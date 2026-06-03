package auth

import (
	"strings"
	"testing"
)

func TestHashAndCheckPassword(t *testing.T) {
	hash, err := HashPassword("s3cret-pw")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if hash == "s3cret-pw" {
		t.Fatal("hash must not equal the plaintext")
	}
	if !CheckPassword(hash, "s3cret-pw") {
		t.Error("correct password should verify")
	}
	if CheckPassword(hash, "wrong") {
		t.Error("wrong password must not verify")
	}
}

func TestScopesForRole(t *testing.T) {
	cases := map[string][]string{
		"owner":    {"read", "write", "admin"},
		"admin":    {"read", "write", "admin"},
		"operator": {"read", "write"},
		"viewer":   {"read"},
		"weird":    {"read"},
	}
	for role, want := range cases {
		got := ScopesForRole(role)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("ScopesForRole(%q) = %v, want %v", role, got, want)
		}
	}
}

func TestSessionTokenFormat(t *testing.T) {
	tok, err := newSessionToken()
	if err != nil {
		t.Fatalf("newSessionToken: %v", err)
	}
	if !strings.HasPrefix(tok, SessionPrefix) {
		t.Errorf("token %q missing prefix %q", tok, SessionPrefix)
	}
	if len(tok) <= len(SessionPrefix) {
		t.Error("token has no random part")
	}
	tok2, _ := newSessionToken()
	if tok == tok2 {
		t.Error("tokens should be unique")
	}
}
