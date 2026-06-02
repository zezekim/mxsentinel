package api

import "testing"

func TestTokenRoundTrip(t *testing.T) {
	token, prefix, hash, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	if PrefixOf(token) != prefix {
		t.Errorf("PrefixOf(%q) = %q, want %q", token, PrefixOf(token), prefix)
	}
	if HashToken(token) != hash {
		t.Errorf("HashToken mismatch")
	}
	if !tokenMatches(token, hash) {
		t.Errorf("tokenMatches should accept the real token")
	}
	if tokenMatches(token+"x", hash) {
		t.Errorf("tokenMatches should reject a tampered token")
	}
	if PrefixOf("not-a-token") != "" {
		t.Errorf("PrefixOf should be empty for a malformed token")
	}
}

func TestBearer(t *testing.T) {
	if tok, ok := bearer("Bearer abc123"); !ok || tok != "abc123" {
		t.Errorf("bearer failed: %q %v", tok, ok)
	}
	if _, ok := bearer("Basic xyz"); ok {
		t.Errorf("bearer should reject non-Bearer schemes")
	}
	if _, ok := bearer(""); ok {
		t.Errorf("bearer should reject empty header")
	}
}

func TestCategorize(t *testing.T) {
	// No snapshot -> everything unknown.
	cats, overall := categorize(false, nil)
	if overall != "unknown" || cats.SPF != "unknown" || cats.MX != "unknown" {
		t.Errorf("no-snapshot: cats=%+v overall=%q", cats, overall)
	}

	// Snapshot, no findings -> all ok, overall healthy.
	cats, overall = categorize(true, nil)
	if overall != "healthy" || cats.SPF != "ok" || cats.DKIM != "ok" {
		t.Errorf("clean: cats=%+v overall=%q", cats, overall)
	}

	// A critical SPF finding -> spf critical, others ok, overall critical.
	cats, overall = categorize(true, []catFinding{{"spf", "critical"}})
	if cats.SPF != "critical" || cats.DKIM != "ok" || overall != "critical" {
		t.Errorf("spf-critical: cats=%+v overall=%q", cats, overall)
	}

	// A DMARC warning -> overall warning.
	cats, overall = categorize(true, []catFinding{{"dmarc", "warning"}})
	if cats.DMARC != "warning" || overall != "warning" {
		t.Errorf("dmarc-warning: cats=%+v overall=%q", cats, overall)
	}

	// Untracked category is ignored, info is treated as ok.
	cats, overall = categorize(true, []catFinding{{"dnssec", "critical"}, {"spf", "info"}})
	if overall != "healthy" || cats.SPF != "ok" {
		t.Errorf("untracked/info: cats=%+v overall=%q", cats, overall)
	}
}
