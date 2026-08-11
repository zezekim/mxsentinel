// Package cpanelplugin implements the MX Sentinel cPanel/WHM plugin. It has two
// runtime modes that share this package:
//
//   - Broker daemon ("serve"): a root-owned process that holds the MX Sentinel API
//     token, listens on a unix socket, and answers requests scoped to the *kernel-
//     reported* uid of the connecting process (SO_PEERCRED). uid 0 (WHM/root) sees
//     the whole tenant; any other uid is mapped to a cPanel account and only sees
//     that account's domains. The token never leaves this process.
//
//   - CGI ("cgi"): a thin front served by cpsrvd to both the WHM plugin (as root)
//     and the cPanel user plugin (as the account's uid). It serves the static
//     dashboard and proxies /api requests to the broker socket. It never reads the
//     token — scope is decided by the broker from the connection's peer credentials,
//     which a user process cannot forge.
//
// This separation is the security boundary: an end user can run code as their own
// uid, but cannot read the root-only config nor impersonate another account over the
// socket, because the broker trusts the kernel's peer uid, not any client-supplied
// identity.
package cpanelplugin

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Default filesystem locations. Overridable via config / env.
const (
	DefaultConfigPath = "/etc/mxsentinel/plugin.conf"
	DefaultSocketPath = "/run/mxsentinel-plugin/api.sock"

	defaultCpanelUsersDir   = "/var/cpanel/users"
	defaultUserDataDomains  = "/etc/userdatadomains"
	defaultCpanelVersionTag = "/usr/local/cpanel/version"
)

// Config is the broker's configuration, read from a root-only file (mode 0600).
type Config struct {
	// APIBase is the MX Sentinel apid base URL, e.g. https://api.mxsentinel.example.com
	APIBase string
	// Token is a tenant API token (mxs_...) with the "read" and "relay" scopes — read for
	// the dashboard views, relay to provision the relay's SMTP submission user. An "admin"
	// token is still accepted (it satisfies every scope) for backward compatibility.
	Token string
	// VerifySSL controls TLS verification when calling APIBase. Default true.
	VerifySSL bool
	// SocketPath is the unix socket the broker listens on / the CGI connects to.
	SocketPath string

	// CpanelUsersDir / UserDataDomains are the cPanel sources used to map a uid's
	// username to the set of domains it owns. Defaults match a standard cPanel box.
	CpanelUsersDir  string
	UserDataDomains string

	// Path is the file this config was read from, so a rotated token can be written
	// back to the same place the daemon was started with.
	Path string
}

// LoadConfig reads the broker config file. An empty path uses DefaultConfigPath.
// Unknown keys are ignored; missing keys fall back to defaults. Recognised keys
// (case-insensitive, "key = value" or "key value", '#' starts a comment):
//
//	api_base, token, verify_ssl, socket_path, cpanel_users_dir, userdata_domains
func LoadConfig(path string) (Config, error) {
	if path == "" {
		path = DefaultConfigPath
	}
	cfg := Config{
		VerifySSL:       true,
		SocketPath:      DefaultSocketPath,
		CpanelUsersDir:  defaultCpanelUsersDir,
		UserDataDomains: defaultUserDataDomains,
		Path:            path,
	}

	f, err := os.Open(path)
	if err != nil {
		return cfg, fmt.Errorf("open config %s: %w", path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val := splitKV(line)
		switch strings.ToLower(key) {
		case "api_base":
			cfg.APIBase = strings.TrimRight(val, "/")
		case "token":
			cfg.Token = val
		case "verify_ssl":
			cfg.VerifySSL = parseBool(val, true)
		case "socket_path":
			if val != "" {
				cfg.SocketPath = val
			}
		case "cpanel_users_dir":
			if val != "" {
				cfg.CpanelUsersDir = val
			}
		case "userdata_domains":
			if val != "" {
				cfg.UserDataDomains = val
			}
		}
	}
	if err := sc.Err(); err != nil {
		return cfg, fmt.Errorf("read config %s: %w", path, err)
	}

	if cfg.APIBase == "" {
		return cfg, fmt.Errorf("config %s: api_base is required", path)
	}
	if cfg.Token == "" {
		return cfg, fmt.Errorf("config %s: token is required", path)
	}
	return cfg, nil
}

// WriteToken rewrites only the `token` line of the config file at path (empty path means
// DefaultConfigPath), leaving every other key, comment, blank line and the file's ordering
// byte-identical.
//
// The write is atomic — temp file in the same directory, fsync, rename — because this file
// is the only durable copy of the API credential. A half-written plugin.conf would lock the
// server out of the API until an operator re-enrolled it by hand, so "partially written"
// must not be a state the file can ever be observed in.
func WriteToken(path, token string) error {
	if path == "" {
		path = DefaultConfigPath
	}
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("write config %s: refusing to write an empty token", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config %s: %w", path, err)
	}

	// The replacement must land with the original's identity: this file is root-only 0600
	// and a widened mode would expose the token to every cPanel user on the box.
	mode := os.FileMode(0o600)
	uid, gid := -1, -1
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			uid, gid = int(st.Uid), int(st.Gid)
		}
	}

	return writeFileAtomic(path, []byte(replaceTokenValue(string(data), token)), mode, uid, gid)
}

// replaceTokenValue returns content with the value of every `token` line replaced,
// preserving each line's indentation, key spelling and separator style. Every occurrence
// is rewritten (not just the last, which is the one LoadConfig would honour) so a
// duplicated key cannot leave a stale secret behind. If the file has no token line at all
// — it should, but a hand-edited config is possible — one is appended rather than
// silently discarding the new credential.
func replaceTokenValue(content, token string) string {
	trailingNewline := strings.HasSuffix(content, "\n")
	body := strings.TrimSuffix(content, "\n")
	var lines []string
	if body != "" {
		lines = strings.Split(body, "\n")
	}

	found := false
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if key, _ := splitKV(t); !strings.EqualFold(key, "token") {
			continue
		}
		lines[i] = setLineValue(line, token)
		found = true
	}
	if !found {
		lines = append(lines, "token = "+token)
		trailingNewline = true
	}

	out := strings.Join(lines, "\n")
	if trailingNewline {
		out += "\n"
	}
	return out
}

// setLineValue replaces the value of a "key = value" or "key value" line, keeping the
// line's indentation, key spelling and separator so the file still looks hand-written.
func setLineValue(line, val string) string {
	indent := len(line) - len(strings.TrimLeft(line, " \t"))
	head, rest := line[:indent], line[indent:]
	if i := strings.IndexByte(rest, '='); i >= 0 {
		return head + rest[:i+1] + " " + val
	}
	if i := strings.IndexAny(rest, " \t"); i >= 0 {
		return head + rest[:i+1] + val
	}
	return head + rest + " = " + val
}

// writeFileAtomic replaces path in one rename, so a reader sees either the old file or
// the new one and never a truncated middle state.
func writeFileAtomic(path string, data []byte, mode os.FileMode, uid, gid int) error {
	dir := filepath.Dir(path)
	// Same directory, or the rename would cross filesystems and stop being atomic.
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("create temp beside %s: %w", path, err)
	}
	name := tmp.Name()
	// No early return may leave a second copy of the credential on disk. After a
	// successful rename this is a no-op.
	defer func() { _ = os.Remove(name) }()

	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod %s: %w", name, err)
	}
	if uid >= 0 && gid >= 0 {
		// Best effort: only root may chown, and root is the only writer in practice.
		_ = os.Chown(name, uid, gid)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", name, err)
	}
	// fsync before the rename, not after: the rename must not be able to publish a file
	// whose contents are still only in the page cache.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("fsync %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", name, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	// Persist the directory entry too, so the rename itself survives a power cut.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// splitKV splits a config line on the first '=' or whitespace run.
func splitKV(line string) (key, val string) {
	if i := strings.IndexByte(line, '='); i >= 0 {
		return strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:])
	}
	if i := strings.IndexAny(line, " \t"); i >= 0 {
		return strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:])
	}
	return line, ""
}

func parseBool(v string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return def
	}
}
