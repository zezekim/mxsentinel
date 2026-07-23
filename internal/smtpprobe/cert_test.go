package smtpprobe

import (
	"crypto/x509"
	"crypto/x509/pkix"
	"testing"
	"time"
)

func leaf(cn, issuer string, notAfter time.Time) *x509.Certificate {
	return &x509.Certificate{
		Subject:   pkix.Name{CommonName: cn},
		Issuer:    pkix.Name{CommonName: issuer},
		NotBefore: notAfter.Add(-90 * 24 * time.Hour),
		NotAfter:  notAfter,
		DNSNames:  []string{cn},
	}
}

func TestInspectCerts(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	warn := 14 * 24 * time.Hour

	tests := []struct {
		name         string
		peer         []*x509.Certificate
		chains       [][]*x509.Certificate
		wantDays     int
		wantExpiring bool
		wantExpired  bool
		wantChain    bool
	}{
		{
			name:         "healthy far-future cert with valid chain",
			peer:         []*x509.Certificate{leaf("mail.example.com", "R3", now.Add(60*24*time.Hour))},
			chains:       [][]*x509.Certificate{{{}}},
			wantDays:     60,
			wantExpiring: false,
			wantChain:    true,
		},
		{
			name:         "within warning window",
			peer:         []*x509.Certificate{leaf("mail.example.com", "R3", now.Add(9*24*time.Hour))},
			wantDays:     9,
			wantExpiring: true,
		},
		{
			name:         "exactly at threshold is expiring",
			peer:         []*x509.Certificate{leaf("m", "i", now.Add(14*24*time.Hour))},
			wantDays:     14,
			wantExpiring: true,
		},
		{
			name:         "already expired",
			peer:         []*x509.Certificate{leaf("m", "i", now.Add(-2*24*time.Hour))},
			wantDays:     -2,
			wantExpiring: true,
			wantExpired:  true,
		},
		{
			name: "no peer certs",
			peer: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := InspectCerts(tt.peer, tt.chains, now, warn)
			if got.DaysUntilExpiry != tt.wantDays {
				t.Errorf("days = %d, want %d", got.DaysUntilExpiry, tt.wantDays)
			}
			if got.Expiring != tt.wantExpiring {
				t.Errorf("expiring = %v, want %v", got.Expiring, tt.wantExpiring)
			}
			if got.Expired != tt.wantExpired {
				t.Errorf("expired = %v, want %v", got.Expired, tt.wantExpired)
			}
			if got.ChainValid != tt.wantChain {
				t.Errorf("chainValid = %v, want %v", got.ChainValid, tt.wantChain)
			}
		})
	}
}

func TestInspectCertsDefaultWarn(t *testing.T) {
	now := time.Now()
	// warn <= 0 falls back to DefaultCertWarnThreshold (14d): a 10d cert must be flagged.
	ci := InspectCerts([]*x509.Certificate{leaf("m", "i", now.Add(10*24*time.Hour))}, nil, now, 0)
	if !ci.Expiring {
		t.Fatalf("expected expiring with default warn threshold")
	}
}

func TestCertSummary(t *testing.T) {
	now := time.Now()
	ci := InspectCerts([]*x509.Certificate{leaf("mail.example.com", "R3", now.Add(9*24*time.Hour))}, nil, now, 14*24*time.Hour)
	if s := ci.CertSummary(); s == "" {
		t.Fatalf("CertSummary empty")
	}
	exp := InspectCerts([]*x509.Certificate{leaf("m", "i", now.Add(-time.Hour))}, nil, now, 14*24*time.Hour)
	if got := exp.CertSummary(); got == "" || !containsWord(got, "EXPIRED") {
		t.Fatalf("CertSummary for expired = %q, want to contain EXPIRED", got)
	}
}

func containsWord(s, w string) bool {
	for i := 0; i+len(w) <= len(s); i++ {
		if s[i:i+len(w)] == w {
			return true
		}
	}
	return false
}
