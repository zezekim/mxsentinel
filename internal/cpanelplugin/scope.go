package cpanelplugin

import (
	"bufio"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

// scopeResolver maps a connecting uid to a cPanel account and the domains it owns.
// All lookups read root-owned cPanel state, so the broker (running as root) is the
// only thing that can perform them — the CGI never touches these files.
type scopeResolver struct {
	usersDir        string
	userDataDomains string
}

func newScopeResolver(cfg Config) *scopeResolver {
	return &scopeResolver{
		usersDir:        cfg.CpanelUsersDir,
		userDataDomains: cfg.UserDataDomains,
	}
}

// usernameForUID resolves a numeric uid to its login name. Returns "" if unknown.
func usernameForUID(uid uint32) string {
	u, err := user.LookupId(strconv.FormatUint(uint64(uid), 10))
	if err != nil {
		return ""
	}
	return u.Username
}

// domainsForUser returns the lowercased set of domains owned by a cPanel account.
// It unions two authoritative sources so it is robust across cPanel versions:
//
//  1. /var/cpanel/users/<user>  — DNS, DNS1, DNS2 … keys (main + addon + parked).
//  2. /etc/userdatadomains      — "domain: owner==user==type==…" lines.
//
// A nil/empty result means "this account owns no monitored domains" — the broker
// then returns an empty (not failed) user view.
func (s *scopeResolver) domainsForUser(username string) map[string]bool {
	out := map[string]bool{}
	if username == "" || strings.ContainsAny(username, "/\\") {
		return out // guard against path traversal via a crafted login name
	}
	s.collectFromUserFile(username, out)
	s.collectFromUserDataDomains(username, out)
	return out
}

// collectFromUserFile parses DNS* keys out of /var/cpanel/users/<user>.
func (s *scopeResolver) collectFromUserFile(username string, out map[string]bool) {
	path := filepath.Join(s.usersDir, username)
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		i := strings.IndexByte(line, '=')
		if i <= 0 {
			continue
		}
		key := line[:i]
		// DNS, DNS1, DNS2, … are the per-account domain keys.
		if key == "DNS" || (strings.HasPrefix(key, "DNS") && isDigits(key[3:])) {
			if d := normalizeDomain(line[i+1:]); d != "" {
				out[d] = true
			}
		}
	}
}

// collectFromUserDataDomains parses /etc/userdatadomains for lines owned by username.
func (s *scopeResolver) collectFromUserDataDomains(username string, out map[string]bool) {
	f, err := os.Open(s.userDataDomains)
	if err != nil {
		return
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		// Format: "domain: owner==user==type==maindomain==docroot==ip==port"
		colon := strings.IndexByte(line, ':')
		if colon <= 0 {
			continue
		}
		domain := normalizeDomain(line[:colon])
		if domain == "" {
			continue
		}
		fields := strings.Split(strings.TrimSpace(line[colon+1:]), "==")
		// owner is field 0; the account user is field 1 in cPanel's layout.
		owner := ""
		if len(fields) > 1 {
			owner = strings.TrimSpace(fields[1])
		} else if len(fields) == 1 {
			owner = strings.TrimSpace(fields[0])
		}
		if owner == username {
			out[domain] = true
		}
	}
}

func normalizeDomain(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimSuffix(s, ".")
	// Skip wildcard/placeholder entries.
	if s == "" || strings.HasPrefix(s, "*") {
		return ""
	}
	return s
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
