package api

import (
	"strings"
	"testing"
	"time"
)

func TestWebmailTokenRoundTrip(t *testing.T) {
	token, prefix, hash, err := GenerateWebmailToken()
	if err != nil {
		t.Fatalf("GenerateWebmailToken: %v", err)
	}
	if got := WebmailTokenPrefixOf(token); got != prefix {
		t.Errorf("WebmailTokenPrefixOf(%q) = %q, want %q", token, got, prefix)
	}
	if !tokenMatches(token, hash) {
		t.Errorf("tokenMatches should accept the real autologin token")
	}
	if tokenMatches(token+"x", hash) {
		t.Errorf("tokenMatches should reject a tampered autologin token")
	}
	// The three token schemes must stay mutually unrecognizable: an autologin token is a
	// live credential handoff and must never be accepted where an API token or a share
	// link is expected, or vice-versa.
	if WebmailTokenPrefixOf("mxs_deadbeef_cafe") != "" {
		t.Errorf("WebmailTokenPrefixOf should reject an api-token (mxs_) prefix")
	}
	if WebmailTokenPrefixOf("mxt_deadbeef_cafe") != "" {
		t.Errorf("WebmailTokenPrefixOf should reject a share-link (mxt_) prefix")
	}
	if PrefixOf(token) != "" {
		t.Errorf("api PrefixOf should not treat a webmail token (mxw_) as its own scheme")
	}
	if ShareTokenPrefixOf(token) != "" {
		t.Errorf("ShareTokenPrefixOf should not treat a webmail token (mxw_) as its own scheme")
	}
	if WebmailTokenPrefixOf("not-a-token") != "" {
		t.Errorf("WebmailTokenPrefixOf should be empty for a malformed token")
	}
}

func TestWebmailTokensAreUnique(t *testing.T) {
	seen := make(map[string]bool, 64)
	for i := 0; i < 64; i++ {
		_, prefix, _, err := GenerateWebmailToken()
		if err != nil {
			t.Fatalf("GenerateWebmailToken: %v", err)
		}
		if seen[prefix] {
			t.Fatalf("duplicate token prefix %q — prefixes are the unique lookup key", prefix)
		}
		seen[prefix] = true
	}
}

func TestRoundcubeHostAndPort(t *testing.T) {
	tests := []struct {
		name     string
		opts     WebmailOptions
		wantHost string
		wantPort int
	}{
		{"starttls default", WebmailOptions{IMAPHost: "mail.example.com", IMAPTLS: "starttls", IMAPPort: 143}, "tls://mail.example.com", 143},
		{"implicit tls", WebmailOptions{IMAPHost: "mail.example.com", IMAPTLS: "tls", IMAPPort: 993}, "ssl://mail.example.com", 993},
		{"plaintext", WebmailOptions{IMAPHost: "dovecot", IMAPTLS: "none", IMAPPort: 143}, "dovecot", 143},
		{"unknown mode falls back to starttls", WebmailOptions{IMAPHost: "h", IMAPTLS: "weird"}, "tls://h", 143},
		{"tls implies 993 when port unset", WebmailOptions{IMAPHost: "h", IMAPTLS: "ssl"}, "ssl://h", 993},
		{"no host", WebmailOptions{IMAPTLS: "starttls"}, "", 143},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.opts.roundcubeHost(); got != tc.wantHost {
				t.Errorf("roundcubeHost() = %q, want %q", got, tc.wantHost)
			}
			if got := tc.opts.roundcubePort(); got != tc.wantPort {
				t.Errorf("roundcubePort() = %d, want %d", got, tc.wantPort)
			}
		})
	}
}

func TestAutologinURL(t *testing.T) {
	// A trailing slash on the configured base must not produce a double slash.
	for _, base := range []string{"https://s.example.com/roundcube", "https://s.example.com/roundcube/"} {
		opts := WebmailOptions{BaseURL: base}
		got := opts.autologinURL("mxw_dead_beef")
		want := "https://s.example.com/roundcube/?_mxs_autologin=mxw_dead_beef"
		if got != want {
			t.Errorf("autologinURL(base=%q) = %q, want %q", base, got, want)
		}
	}
	// Tokens are hex + underscores today, but the URL builder must stay escape-safe.
	if got := (WebmailOptions{BaseURL: "https://x/rc"}).autologinURL("a b&c"); !strings.Contains(got, "a+b%26c") {
		t.Errorf("autologinURL should query-escape the token, got %q", got)
	}
}

func TestWebmailTokenTTL(t *testing.T) {
	if got := (WebmailOptions{}).tokenTTL(); got != defaultWebmailTokenTTL {
		t.Errorf("tokenTTL() with no config = %v, want the %v default", got, defaultWebmailTokenTTL)
	}
	if got := (WebmailOptions{TokenTTL: -5 * time.Second}).tokenTTL(); got != defaultWebmailTokenTTL {
		t.Errorf("a negative TTL must fall back to the default, got %v", got)
	}
	if got := (WebmailOptions{TokenTTL: 90 * time.Second}).tokenTTL(); got != 90*time.Second {
		t.Errorf("tokenTTL() = %v, want the configured 90s", got)
	}
}

func TestWebmailEnabledRequiresBothSettings(t *testing.T) {
	tests := []struct {
		name string
		opts WebmailOptions
		want bool
	}{
		{"unconfigured", WebmailOptions{}, false},
		{"url only", WebmailOptions{BaseURL: "https://x/rc"}, false},
		{"secret only", WebmailOptions{PluginSecret: "s3cret"}, false},
		{"both", WebmailOptions{BaseURL: "https://x/rc", PluginSecret: "s3cret"}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{webmail: tc.opts}
			if got := s.webmailEnabled(); got != tc.want {
				t.Errorf("webmailEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}
