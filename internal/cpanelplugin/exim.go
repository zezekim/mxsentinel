package cpanelplugin

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// This file manages cPanel's rebuild-safe Exim overlay (/etc/exim.conf.local) to route
// outbound mail through the MX Sentinel relay. Correctness here is load-bearing: a bad
// edit can break outbound mail for every account on the server, so every mutation backs
// up the file, rebuilds, validates with `exim -bV`, and rolls back on any failure.

const (
	defaultEximLocalPath = "/etc/exim.conf.local"

	// cPanel exim.conf.local section marker tokens. Routers go in POSTMAILCOUNT (not
	// ROUTERSTART) so cPanel's per-domain hourly mail limits still apply before relaying.
	markerRouter    = "@POSTMAILCOUNT@"
	markerTransport = "@TRANSPORTSTART@"
	markerAuth      = "@AUTH@"

	// Sentinels delimit our managed blocks so they can be removed byte-for-byte.
	sentinelBegin = "# BEGIN mxsentinel"
	sentinelEnd   = "# END mxsentinel"

	buildEximConf = "/usr/local/cpanel/scripts/buildeximconf"
	restartExim   = "/usr/local/cpanel/scripts/restartsrv_exim"
	eximBinary    = "/usr/sbin/exim"
)

// cmdRunner runs an external command; abstracted so tests can stub cPanel scripts.
type cmdRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

func execRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.CombinedOutput()
}

// eximManager edits the overlay and drives the cPanel rebuild/validate/restart cycle.
type eximManager struct {
	localPath string
	run       cmdRunner
}

func newEximManager() *eximManager {
	return &eximManager{localPath: defaultEximLocalPath, run: execRunner}
}

// smarthostBlocks returns the router/transport/authenticator blocks for the given relay
// and credential, keyed by the section marker each belongs under.
func smarthostBlocks(host string, port int, username, password string) map[string]string {
	router := fmt.Sprintf(`mxsentinel_smarthost:
  driver = manualroute
  domains = ! +local_domains
  transport = mxsentinel_smtp
  route_list = * %s::%d
  no_more
`, host, port)

	// DKIM must be signed here, before the message leaves the cPanel box: routing to this
	// custom transport bypasses cPanel's stock remote_smtp transport (which is where cPanel
	// applies its per-domain signing), so if we don't sign, relayed mail arrives unsigned and
	// never gets dkim=pass. We sign with the domain's own cPanel key under selector `default`
	// (/var/cpanel/domain_keys/private/<domain>), whose public record cPanel auto-publishes
	// when the domain's DNS is local. The ${if exists ...}{0} guard makes signing a no-op for
	// any domain that has no key rather than failing the transport.
	transport := fmt.Sprintf(`mxsentinel_smtp:
  driver = smtp
  hosts_require_auth = %s
  hosts_require_tls = %s
  dkim_domain = ${sender_address_domain}
  dkim_selector = default
  dkim_private_key = ${if exists{/var/cpanel/domain_keys/private/${sender_address_domain}}{/var/cpanel/domain_keys/private/${sender_address_domain}}{0}}
  dkim_canon = relaxed
`, host, host)

	// client_send carries the SASL credential inline (as cPanel smarthost guides do).
	auth := fmt.Sprintf(`mxsentinel_login:
  driver = plaintext
  public_name = LOGIN
  client_send = : %s : %s
`, username, password)

	return map[string]string{
		markerRouter:    router,
		markerTransport: transport,
		markerAuth:      auth,
	}
}

// wrap turns a raw config block into a sentinel-delimited region (trailing newline so
// the region is a whole number of lines, enabling exact removal).
func wrap(block string) string {
	return sentinelBegin + "\n" + strings.TrimRight(block, "\n") + "\n" + sentinelEnd + "\n"
}

// insertBlocks inserts each block immediately after its section marker line. It is pure
// (no I/O) and idempotent: any pre-existing mxsentinel regions are removed first, so
// re-applying yields the same result. Returns an error if any marker line is absent.
func insertBlocks(content string, blocks map[string]string) (string, error) {
	content = removeBlocks(content)
	lines := strings.Split(content, "\n")

	for marker, block := range blocks {
		idx := indexOfLine(lines, marker)
		if idx < 0 {
			return "", fmt.Errorf("exim.conf.local: section marker %q not found — refusing to guess file layout", marker)
		}
		wrapped := strings.Split(strings.TrimRight(wrap(block), "\n"), "\n")
		// Insert right after the marker line.
		out := make([]string, 0, len(lines)+len(wrapped))
		out = append(out, lines[:idx+1]...)
		out = append(out, wrapped...)
		out = append(out, lines[idx+1:]...)
		lines = out
	}
	return strings.Join(lines, "\n"), nil
}

// removeBlocks strips every sentinel-delimited mxsentinel region (inclusive). Pure and
// exactly reverses insertBlocks, so insert→remove round-trips to the original bytes.
func removeBlocks(content string) string {
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	inBlock := false
	for _, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		switch {
		case trimmed == sentinelBegin:
			inBlock = true
		case trimmed == sentinelEnd:
			inBlock = false
		case !inBlock:
			out = append(out, ln)
		}
	}
	return strings.Join(out, "\n")
}

// hasBlocks reports whether the overlay currently contains an mxsentinel region.
func hasBlocks(content string) bool {
	for _, ln := range strings.Split(content, "\n") {
		if strings.TrimSpace(ln) == sentinelBegin {
			return true
		}
	}
	return false
}

func indexOfLine(lines []string, want string) int {
	for i, ln := range lines {
		if strings.TrimSpace(ln) == want {
			return i
		}
	}
	return -1
}

// enabled reports whether the smarthost overlay is currently installed.
func (m *eximManager) enabled() (bool, error) {
	content, err := os.ReadFile(m.localPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return hasBlocks(string(content)), nil
}

// apply installs (or refreshes) the smarthost blocks and rebuilds Exim. On any failure
// after the file is written, it restores the backup and rebuilds so mail keeps flowing.
func (m *eximManager) apply(ctx context.Context, host string, port int, username, password string) error {
	return m.mutate(ctx, func(content string) (string, error) {
		return insertBlocks(content, smarthostBlocks(host, port, username, password))
	})
}

// remove strips the smarthost blocks and rebuilds Exim (returns to direct delivery).
func (m *eximManager) remove(ctx context.Context) error {
	return m.mutate(ctx, func(content string) (string, error) {
		return removeBlocks(content), nil
	})
}

// mutate is the safe edit cycle: backup → write → buildeximconf → validate → restart,
// rolling back to the backup if the rebuild or validation fails.
func (m *eximManager) mutate(ctx context.Context, transform func(string) (string, error)) error {
	orig, err := os.ReadFile(m.localPath)
	if err != nil {
		return fmt.Errorf("read %s: %w (is this a cPanel server?)", m.localPath, err)
	}
	next, err := transform(string(orig))
	if err != nil {
		return err
	}
	if string(orig) == next {
		return nil // nothing to do
	}

	backup := fmt.Sprintf("%s.mxsentinel.bak.%d", m.localPath, time.Now().Unix())
	if err := os.WriteFile(backup, orig, 0o640); err != nil {
		return fmt.Errorf("write backup %s: %w", backup, err)
	}
	if err := os.WriteFile(m.localPath, []byte(next), 0o640); err != nil {
		return fmt.Errorf("write %s: %w", m.localPath, err)
	}

	if err := m.rebuildAndValidate(ctx); err != nil {
		// Roll back: restore the original overlay and rebuild so Exim is left healthy.
		_ = os.WriteFile(m.localPath, orig, 0o640)
		if rbErr := m.rebuildAndValidate(ctx); rbErr != nil {
			return fmt.Errorf("CONFIG REBUILD FAILED AND ROLLBACK ALSO FAILED: %v (rollback: %v) — backup at %s; inspect Exim immediately", err, rbErr, backup)
		}
		return fmt.Errorf("config rebuild/validation failed, rolled back to working config (backup %s): %w", backup, err)
	}

	if out, err := m.run(ctx, restartExim); err != nil {
		return fmt.Errorf("restart exim: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// rebuildAndValidate runs buildeximconf then parses the built config with `exim -bV`.
func (m *eximManager) rebuildAndValidate(ctx context.Context) error {
	if out, err := m.run(ctx, buildEximConf); err != nil {
		return fmt.Errorf("buildeximconf: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := m.run(ctx, eximBinary, "-bV"); err != nil {
		return fmt.Errorf("exim -bV rejected the built config: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
