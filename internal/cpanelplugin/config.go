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
	"strings"
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
