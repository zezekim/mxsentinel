package bimi

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"
)

func TestParseRecord(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantVer string
		wantL   string
		wantA   string
		wantOK  bool
	}{
		{"empty", "", "", "", "", false},
		{"logo only", "v=BIMI1; l=https://ex.com/logo.svg", "BIMI1", "https://ex.com/logo.svg", "", true},
		{"logo and vmc", "v=BIMI1; l=https://ex.com/logo.svg; a=https://ex.com/vmc.pem",
			"BIMI1", "https://ex.com/logo.svg", "https://ex.com/vmc.pem", true},
		{"messy spacing", "  v = BIMI1 ;l= https://ex.com/l.svg ;", "BIMI1", "https://ex.com/l.svg", "", true},
		{"declination empty l", "v=BIMI1; l=", "BIMI1", "", "", true},
		{"wrong version", "v=BIMI2; l=https://ex.com/l.svg", "BIMI2", "https://ex.com/l.svg", "", false},
		{"not bimi", "v=spf1 -all", "SPF1 -ALL", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseRecord(tt.in)
			if got.Version != tt.wantVer || got.LogoURL != tt.wantL || got.VMCURL != tt.wantA || got.Valid != tt.wantOK {
				t.Errorf("ParseRecord(%q) = %+v", tt.in, got)
			}
		})
	}
}

func TestDMARCEnforced(t *testing.T) {
	tests := []struct {
		in         string
		wantEnf    bool
		wantPolicy string
	}{
		{"", false, ""},
		{"v=DMARC1; p=none; rua=mailto:x@y.com", false, "none"},
		{"v=DMARC1; p=quarantine", true, "quarantine"},
		{"v=DMARC1; p=reject; pct=100", true, "reject"},
		{"v=DMARC1; p=REJECT", true, "reject"},
		{"v=spf1 -all", false, ""},
		{"v=DMARC1", false, ""},
	}
	for _, tt := range tests {
		enf, pol := DMARCEnforced(tt.in)
		if enf != tt.wantEnf || pol != tt.wantPolicy {
			t.Errorf("DMARCEnforced(%q) = (%v, %q), want (%v, %q)", tt.in, enf, pol, tt.wantEnf, tt.wantPolicy)
		}
	}
}

const validSVG = `<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" baseProfile="tiny-ps" version="1.2" viewBox="0 0 100 100">
  <title>Example</title>
  <rect width="100" height="100" fill="#0a0"/>
</svg>`

func TestValidateSVG(t *testing.T) {
	tests := []struct {
		name         string
		in           string
		wantProblems bool
		wantContains string
	}{
		{"valid", validSVG, false, ""},
		{"empty", "", true, "empty"},
		{"not svg", "<html></html>", true, "not an SVG"},
		{"missing tiny-ps", `<svg version="1.2"><title>x</title></svg>`, true, "tiny-ps"},
		{"missing version", `<svg baseProfile="tiny-ps"><title>x</title></svg>`, true, `version="1.2"`},
		{"missing title", `<svg baseProfile="tiny-ps" version="1.2"></svg>`, true, "title"},
		{"has script", `<svg baseProfile="tiny-ps" version="1.2"><title>x</title><script>a()</script></svg>`, true, "script"},
		{"has external image", `<svg baseProfile="tiny-ps" version="1.2"><title>x</title><image href="https://e/x.png"/></svg>`, true, "image"},
		{"has animation", `<svg baseProfile="tiny-ps" version="1.2"><title>x</title><animate/></svg>`, true, "animation"},
		{"external href", `<svg baseProfile="tiny-ps" version="1.2"><title>x</title><a href="https://e"/></svg>`, true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			problems := ValidateSVG([]byte(tt.in))
			if (len(problems) > 0) != tt.wantProblems {
				t.Fatalf("ValidateSVG(%q) problems=%v, wantProblems=%v", tt.name, problems, tt.wantProblems)
			}
			if tt.wantContains != "" {
				joined := strings.Join(problems, "|")
				if !strings.Contains(joined, tt.wantContains) {
					t.Errorf("problems %q do not contain %q", joined, tt.wantContains)
				}
			}
		})
	}
}

func TestValidateSVGTooLarge(t *testing.T) {
	big := "<svg baseProfile=\"tiny-ps\" version=\"1.2\"><title>x</title>" +
		strings.Repeat("<rect/>", 6000) + "</svg>"
	problems := ValidateSVG([]byte(big))
	found := false
	for _, p := range problems {
		if strings.Contains(p, "over the") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected size-limit problem, got %v", problems)
	}
}

// makeVMC builds a self-signed PEM certificate with the given expiry for VMC tests.
func makeVMC(t *testing.T, notAfter time.Time) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "Example VMC"},
		NotBefore:    notAfter.Add(-365 * 24 * time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestParseVMCExpiry(t *testing.T) {
	want := time.Date(2027, 1, 2, 0, 0, 0, 0, time.UTC)
	pemBytes := makeVMC(t, want)
	got, err := ParseVMCExpiry(pemBytes)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(want) {
		t.Errorf("expiry = %v, want %v", got, want)
	}

	if _, err := ParseVMCExpiry([]byte("not a pem")); err == nil {
		t.Error("expected error for non-PEM input")
	}
}

func TestAssessReadiness(t *testing.T) {
	now := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	future := makeVMC(t, now.Add(90*24*time.Hour))
	past := makeVMC(t, now.Add(-24*time.Hour))
	goodLogo := &Artifact{Fetched: true, Body: []byte(validSVG)}
	badLogo := &Artifact{Fetched: true, Body: []byte("<html/>")}
	dmarcEnf := "v=DMARC1; p=reject"
	dmarcNone := "v=DMARC1; p=none"

	tests := []struct {
		name      string
		record    string
		dmarc     string
		logo      *Artifact
		vmc       *Artifact
		wantState string
	}{
		{"no record", "", dmarcEnf, nil, nil, StateNotConfigured},
		{"record but dmarc none", "v=BIMI1; l=https://e/l.svg", dmarcNone, goodLogo, nil, StateBlocked},
		{"record, enforced, no vmc", "v=BIMI1; l=https://e/l.svg", dmarcEnf, goodLogo, nil, StatePartial},
		{"bad logo", "v=BIMI1; l=https://e/l.svg", dmarcEnf, badLogo, nil, StateBlocked},
		{"full ready", "v=BIMI1; l=https://e/l.svg; a=https://e/v.pem", dmarcEnf, goodLogo,
			&Artifact{Fetched: true, Body: future}, StateReady},
		{"vmc expired", "v=BIMI1; l=https://e/l.svg; a=https://e/v.pem", dmarcEnf, goodLogo,
			&Artifact{Fetched: true, Body: past}, StateVMCExpired},
		{"vmc fetch failed", "v=BIMI1; l=https://e/l.svg; a=https://e/v.pem", dmarcEnf, goodLogo,
			&Artifact{Fetched: true, Err: "HTTP 404"}, StateBlocked},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rep := Assess("example.com", tt.record, tt.dmarc, tt.logo, tt.vmc, now)
			if rep.Readiness != tt.wantState {
				t.Errorf("readiness = %q, want %q; checklist=%+v", rep.Readiness, tt.wantState, rep.Checklist)
			}
			if len(rep.Checklist) == 0 {
				t.Error("expected a non-empty checklist")
			}
		})
	}
}

// --- Inspect with fakes (no network) ---------------------------------------

type fakeResolver struct{ txt map[string][]string }

func (f fakeResolver) TXT(_ context.Context, name string) ([]string, error) {
	return f.txt[name], nil
}

type fakeFetcher struct{ bodies map[string][]byte }

func (f fakeFetcher) Fetch(_ context.Context, url string) ([]byte, error) {
	if b, ok := f.bodies[url]; ok {
		return b, nil
	}
	return nil, context.DeadlineExceeded
}

func TestInspect(t *testing.T) {
	res := fakeResolver{txt: map[string][]string{
		"default._bimi.example.com": {"v=BIMI1; l=https://e/logo.svg"},
	}}
	fet := fakeFetcher{bodies: map[string][]byte{
		"https://e/logo.svg": []byte(validSVG),
	}}
	rep, err := Inspect(context.Background(), res, fet, "example.com", "v=DMARC1; p=quarantine")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Readiness != StatePartial {
		t.Errorf("readiness = %q, want %q", rep.Readiness, StatePartial)
	}
	if !rep.DMARCEnforced {
		t.Error("expected DMARCEnforced true")
	}
	if rep.Record.LogoURL != "https://e/logo.svg" {
		t.Errorf("logo url = %q", rep.Record.LogoURL)
	}
}
