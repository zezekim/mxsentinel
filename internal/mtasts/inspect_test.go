package mtasts

import (
	"context"
	"errors"
	"testing"
	"time"
)

// --- fixtures/fakes (no network) ---

type fakeResolver struct {
	txt map[string][]string
	err error
}

func (f fakeResolver) TXT(_ context.Context, name string) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.txt[name], nil
}

type fakeFetcher struct {
	body string
	err  error
}

func (f fakeFetcher) Fetch(_ context.Context, _ string) (string, error) {
	return f.body, f.err
}

type fakeCerts struct{ byHost map[string]CertInfo }

func (f fakeCerts) Check(_ context.Context, host string) CertInfo {
	if c, ok := f.byHost[host]; ok {
		return c
	}
	return CertInfo{Host: host, Err: "unreachable"}
}

func codes(s Snapshot) map[string]string {
	m := map[string]string{}
	for _, f := range s.Findings {
		m[f.Code] = f.Severity
	}
	return m
}

func TestInspect(t *testing.T) {
	future := time.Now().Add(90 * 24 * time.Hour)
	soon := time.Now().Add(3 * 24 * time.Hour)
	past := time.Now().Add(-24 * time.Hour)

	base := fakeResolver{txt: map[string][]string{
		"_mta-sts.example.com": {"v=STSv1; id=20240101T000000Z"},
	}}

	t.Run("healthy enforce policy", func(t *testing.T) {
		d := Deps{
			Resolver: base,
			Fetcher:  fakeFetcher{body: "version: STSv1\nmode: enforce\nmx: mx1.example.com\nmax_age: 86400\n"},
			CertChecker: fakeCerts{byHost: map[string]CertInfo{
				"mx1.example.com": {Host: "mx1.example.com", Valid: true, NotAfter: future},
			}},
		}
		s, err := Inspect(context.Background(), d, "example.com", Options{})
		if err != nil {
			t.Fatal(err)
		}
		if !s.Healthy {
			t.Errorf("expected healthy, findings=%v", codes(s))
		}
		if s.State.Mode != ModeEnforce {
			t.Errorf("mode = %q", s.State.Mode)
		}
		if s.CertExpiry.IsZero() {
			t.Errorf("expected cert expiry to be set")
		}
		if s.Checksum == "" {
			t.Errorf("expected checksum")
		}
	})

	t.Run("missing TXT flagged", func(t *testing.T) {
		d := Deps{
			Resolver: fakeResolver{txt: map[string][]string{}},
			Fetcher:  fakeFetcher{err: errors.New("404")},
		}
		s, _ := Inspect(context.Background(), d, "example.com", Options{})
		c := codes(s)
		if _, ok := c[CodeTXTMissing]; !ok {
			t.Errorf("want TXT missing finding, got %v", c)
		}
		if _, ok := c[CodePolicyUnreachable]; !ok {
			t.Errorf("want policy unreachable finding, got %v", c)
		}
	})

	t.Run("mode none warns", func(t *testing.T) {
		d := Deps{
			Resolver: base,
			Fetcher:  fakeFetcher{body: "version: STSv1\nmode: none\nmax_age: 0\n"},
		}
		s, _ := Inspect(context.Background(), d, "example.com", Options{})
		if codes(s)[CodeModeNotEnforced] != SevWarning {
			t.Errorf("want mode-not-enforced warning, got %v", codes(s))
		}
	})

	t.Run("expired cert is critical", func(t *testing.T) {
		d := Deps{
			Resolver: base,
			Fetcher:  fakeFetcher{body: "version: STSv1\nmode: enforce\nmx: mx1.example.com\nmax_age: 1\n"},
			CertChecker: fakeCerts{byHost: map[string]CertInfo{
				"mx1.example.com": {Host: "mx1.example.com", Valid: true, NotAfter: past},
			}},
		}
		s, _ := Inspect(context.Background(), d, "example.com", Options{})
		if s.Healthy {
			t.Errorf("expected unhealthy on expired cert")
		}
		if codes(s)[CodeCertExpired] != SevCritical {
			t.Errorf("want cert-expired critical, got %v", codes(s))
		}
	})

	t.Run("expiring soon warns", func(t *testing.T) {
		d := Deps{
			Resolver: base,
			Fetcher:  fakeFetcher{body: "version: STSv1\nmode: enforce\nmx: mx1.example.com\nmax_age: 1\n"},
			CertChecker: fakeCerts{byHost: map[string]CertInfo{
				"mx1.example.com": {Host: "mx1.example.com", Valid: true, NotAfter: soon},
			}},
		}
		s, _ := Inspect(context.Background(), d, "example.com", Options{CertWarnDays: 14})
		if codes(s)[CodeCertExpiringSoon] != SevWarning {
			t.Errorf("want cert-expiring-soon warning, got %v", codes(s))
		}
	})

	t.Run("invalid policy is critical", func(t *testing.T) {
		d := Deps{
			Resolver: base,
			Fetcher:  fakeFetcher{body: "not a policy at all"},
		}
		s, _ := Inspect(context.Background(), d, "example.com", Options{})
		if s.Healthy {
			t.Errorf("expected unhealthy on invalid policy")
		}
		if codes(s)[CodePolicyInvalid] != SevCritical {
			t.Errorf("want policy-invalid critical, got %v", codes(s))
		}
	})

	t.Run("wildcard mx skipped for cert check", func(t *testing.T) {
		called := false
		d := Deps{
			Resolver: base,
			Fetcher:  fakeFetcher{body: "version: STSv1\nmode: enforce\nmx: *.example.net\nmax_age: 1\n"},
			CertChecker: certFunc(func(host string) CertInfo {
				called = true
				return CertInfo{Host: host, Valid: true, NotAfter: future}
			}),
		}
		Inspect(context.Background(), d, "example.com", Options{})
		if called {
			t.Errorf("cert checker should not be called for wildcard mx")
		}
	})
}

type certFunc func(host string) CertInfo

func (f certFunc) Check(_ context.Context, host string) CertInfo { return f(host) }
