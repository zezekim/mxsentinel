package relayfailover

import (
	"fmt"
	"os"
	"path/filepath"
)

// Rendered file names written into the bind-mounted state dir (next to the domains file) for
// the host-side hook to apply to Postfix. Keeping credentials in the state dir (not the API
// path) means the container never touches host Postfix, and Postfix keeps running on the last
// applied config even if MX Sentinel is down.
const (
	SASLFileName      = "smarthost.sasl"      // "[host]:port user:pass"  (mode 0600)
	TransportFileName = "smarthost.transport" // "FALLBACK_TRANSPORT=relay-mailbaby:[host]:port"
)

// RenderSmarthost writes the SASL credential line and the transport nexthop into the state
// dir (the directory containing the domains state file) for the host hook to install into
// Postfix. The SASL key format is "[host]:port" (brackets on host only, port outside) so it
// matches the transport nexthop — the exact bug that made hand-wired creds fail. Files are
// written atomically; the SASL file is chmod 0600.
//
// A blank host/user/pass clears both files (removes them), so disabling the smarthost in the
// dashboard tears the credentials back down on the next hook run.
func RenderSmarthost(stateDir, host string, port int, username, password string) error {
	saslPath := filepath.Join(stateDir, SASLFileName)
	transPath := filepath.Join(stateDir, TransportFileName)

	if host == "" || username == "" || password == "" {
		_ = os.Remove(saslPath)
		_ = os.Remove(transPath)
		return nil
	}
	if port <= 0 {
		port = 587
	}

	sasl := fmt.Sprintf("[%s]:%d %s:%s\n", host, port, username, password)
	transport := fmt.Sprintf("FALLBACK_TRANSPORT=relay-mailbaby:[%s]:%d\n", host, port)

	if err := writeFileAtomic(saslPath, sasl, 0o600); err != nil {
		return fmt.Errorf("render smarthost sasl: %w", err)
	}
	if err := writeFileAtomic(transPath, transport, 0o644); err != nil {
		return fmt.Errorf("render smarthost transport: %w", err)
	}
	return nil
}

func writeFileAtomic(path, content string, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".render-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
