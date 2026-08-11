package api

import (
	"net/http"
	"testing"
	"time"
)

// The escalation guard is the security boundary of the whole enrollment feature: it is what
// stops the enrollment secret shipped to every new server from being able to mint itself a
// tenant-wide admin credential. These tests exist mainly to make a future widening of
// provisionableScopes or provisionedNamePattern fail loudly.
func TestAuthorizeMintRejectsEscalation(t *testing.T) {
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	provision := []string{ScopeProvision}

	escalations := []struct {
		name   string
		scopes []string
	}{
		{"admin outright", []string{ScopeAdmin}},
		{"admin smuggled alongside allowed scopes", []string{ScopeRead, ScopeRelay, ScopeAdmin}},
		{"write", []string{ScopeWrite}},
		{"provision (minting the power to mint)", []string{ScopeProvision}},
		{"read plus provision", []string{ScopeRead, ScopeProvision}},
	}
	for _, tc := range escalations {
		t.Run(tc.name, func(t *testing.T) {
			_, err := authorizeMint(provision, "cpanel-host.example.com", tc.scopes, "", now)
			if err == nil {
				t.Fatalf("provision caller was allowed to mint %v — privilege escalation", tc.scopes)
			}
			if err.status != http.StatusForbidden {
				t.Errorf("status = %d, want 403", err.status)
			}
		})
	}
}

func TestAuthorizeMintNameConstraint(t *testing.T) {
	now := time.Now()
	provision := []string{ScopeProvision}
	allowed := []string{ScopeRead, ScopeRelay}

	valid := []string{"cpanel-host.example.com", "cpanel-a", "cpanel-web01", "cpanel-1.2.3.4"}
	for _, n := range valid {
		if _, err := authorizeMint(provision, n, allowed, "", now); err != nil {
			t.Errorf("name %q should be allowed, got %v", n, err.message)
		}
	}

	// The prefix pin matters: without it a leaked enrollment token could name its key
	// "dashboard" and, via the reissue path, silently rotate the operator's own credential
	// out from under them.
	invalid := []string{
		"dashboard",                  // no cpanel- prefix — the attack the pin prevents
		"cpanel-",                    // prefix only
		"CPANEL-host",                // uppercase
		"cpanel-host name",           // space
		"cpanel--host",               // second char must be alphanumeric
		"../cpanel-host",             // path-ish
		"cpanel-host\nX-Injected: 1", // newline
		"",                           // empty
	}
	for _, n := range invalid {
		if _, err := authorizeMint(provision, n, allowed, "", now); err == nil {
			t.Errorf("name %q should have been rejected", n)
		}
	}
}

func TestAuthorizeMintForcesProvisionExpiry(t *testing.T) {
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)

	// A provision caller never gets a non-expiring credential, and cannot talk its way into
	// one by asking for a longer lifetime.
	for _, requested := range []string{"", "87600h", "-1h", "garbage"} {
		d, err := authorizeMint([]string{ScopeProvision}, "cpanel-host", []string{ScopeRead, ScopeRelay}, requested, now)
		if err != nil {
			t.Fatalf("expires_in %q: unexpected refusal: %v", requested, err.message)
		}
		if d.expiresAt == nil {
			t.Fatalf("expires_in %q: provision-minted key must expire", requested)
		}
		if want := now.Add(provisionedKeyTTL).UTC(); !d.expiresAt.Equal(want) {
			t.Errorf("expires_in %q: expiry = %v, want forced %v", requested, d.expiresAt, want)
		}
		if d.isAdmin {
			t.Errorf("expires_in %q: provision caller must not be treated as admin", requested)
		}
	}
}

func TestAuthorizeMintAdminIsUnconstrained(t *testing.T) {
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	admin := []string{ScopeAdmin}

	// Any scopes, any name, and no forced expiry.
	d, err := authorizeMint(admin, "dashboard", []string{ScopeAdmin, ScopeWrite}, "", now)
	if err != nil {
		t.Fatalf("admin refused: %v", err.message)
	}
	if !d.isAdmin {
		t.Error("isAdmin should be true for an admin caller")
	}
	if d.expiresAt != nil {
		t.Errorf("admin key with no expires_in should never expire, got %v", d.expiresAt)
	}

	d, err = authorizeMint(admin, "ci-runner", []string{ScopeRead}, "24h", now)
	if err != nil {
		t.Fatalf("admin with expires_in refused: %v", err.message)
	}
	if want := now.Add(24 * time.Hour).UTC(); d.expiresAt == nil || !d.expiresAt.Equal(want) {
		t.Errorf("expiry = %v, want %v", d.expiresAt, want)
	}

	if _, err := authorizeMint(admin, "x", []string{ScopeRead}, "nonsense", now); err == nil {
		t.Error("a malformed expires_in should be a 400 even for an admin")
	} else if err.status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", err.status)
	}
}

// A caller with neither admin nor provision never reaches authorizeMint (requireScope stops
// them), but the guard must still refuse rather than fall through to the permissive branch.
func TestAuthorizeMintUnprivilegedCallerTreatedAsProvision(t *testing.T) {
	if _, err := authorizeMint([]string{ScopeRead}, "dashboard", []string{ScopeAdmin}, "", time.Now()); err == nil {
		t.Fatal("a read-only caller must not be able to mint an admin key")
	}
}

func TestHasLiteralScope(t *testing.T) {
	// The distinction this whole feature rests on: hasScope treats admin as a wildcard,
	// hasLiteralScope does not.
	if !hasScope([]string{ScopeAdmin}, ScopeProvision) {
		t.Error("hasScope: admin should satisfy provision")
	}
	if hasLiteralScope([]string{ScopeAdmin}, ScopeProvision) {
		t.Error("hasLiteralScope: admin must NOT count as literally holding provision")
	}
	if !hasLiteralScope([]string{ScopeRead, ScopeAdmin}, ScopeAdmin) {
		t.Error("hasLiteralScope: should find a scope that is literally present")
	}
	// The retarget's backward-compatibility claim: existing admin tokens still pass the
	// relay-gated smtp-user routes without being reissued.
	if !hasScope([]string{ScopeAdmin}, ScopeRelay) {
		t.Error("existing admin tokens must still satisfy the relay-gated routes")
	}
	if hasScope([]string{ScopeRead}, ScopeRelay) {
		t.Error("a read-only token must not satisfy relay")
	}
}
