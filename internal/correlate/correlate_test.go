package correlate

import (
	"strings"
	"testing"
	"time"

	"github.com/zezekim/mxsentinel/pkg/contracts"
)

func TestClassifyRejection(t *testing.T) {
	cases := []struct {
		name     string
		code     int
		enhanced string
		text     string
		provider string
		want     ReasonCategory
	}{
		{"spf fail", 550, "5.7.1", "550 5.7.1 SPF check failed for example.com", "google", ReasonAuth},
		{"gmail unauth", 550, "5.7.26", "550 5.7.26 Unauthenticated email is not accepted", "google", ReasonAuth},
		{"spamhaus", 554, "5.7.1", "554 5.7.1 Service unavailable; blocked using Spamhaus", "other", ReasonBlocklist},
		{"blacklist", 553, "", "553 your IP is on a blacklist", "other", ReasonBlocklist},
		{"ms reputation", 550, "5.7.1", "550 5.7.1 ... high probability of spam ... S3140", "microsoft", ReasonReputation},
		{"yahoo complaints", 421, "4.7.0", "421 4.7.0 [TS03] deferred due to user complaints", "yahoo", ReasonReputation},
		{"rate limit", 421, "4.7.0", "421 4.7.0 try again later, too many connections", "other", ReasonRateLimit},
		{"greylist", 450, "4.2.0", "450 greylisted, please try again later", "other", ReasonGreylist},
		{"user unknown", 550, "5.1.1", "550 5.1.1 user unknown", "other", ReasonUserUnknown},
		{"mailbox full", 552, "5.2.2", "552 5.2.2 mailbox full", "other", ReasonMailboxFull},
		{"spam content", 550, "5.7.1", "550 5.7.1 message identified as spam", "other", ReasonSpamContent},
		{"tls", 530, "", "530 must issue a STARTTLS command first", "other", ReasonTLS},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ClassifyRejection(c.code, c.enhanced, c.text, c.provider)
			if got.Category != c.want {
				t.Errorf("category = %q, want %q", got.Category, c.want)
			}
		})
	}

	if !ClassifyRejection(421, "4.7.0", "deferred", "other").Transient {
		t.Error("421 should be transient")
	}
	if ClassifyRejection(550, "5.7.1", "blocked", "other").Transient {
		t.Error("550 should not be transient")
	}
}

func TestDetectSpike(t *testing.T) {
	cfg := DefaultSpikeConfig()
	cases := []struct {
		name     string
		recent   Window
		baseline Window
		want     bool
	}{
		{"clear spike vs low baseline", Window{Total: 100, Rejections: 40}, Window{Total: 100, Rejections: 2}, true},
		{"below min rejections", Window{Total: 100, Rejections: 3}, Window{Total: 100, Rejections: 0}, false},
		{"rate below threshold", Window{Total: 100, Rejections: 20}, Window{Total: 100, Rejections: 0}, false},
		{"zero baseline elevated", Window{Total: 50, Rejections: 30}, Window{}, true},
		{"elevated but not anomalous", Window{Total: 100, Rejections: 40}, Window{Total: 100, Rejections: 35}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DetectSpike(c.recent, c.baseline, cfg).IsSpike; got != c.want {
				t.Errorf("IsSpike = %v, want %v", got, c.want)
			}
		})
	}
}

func ts(h, m int) time.Time { return time.Date(2026, 6, 3, h, m, 0, 0, time.UTC) }

// The architecture's flagship scenario: a DKIM selector removed at 10:44, then Microsoft
// auth rejections starting 10:45 -> high-confidence DNS root cause.
func TestCorrelateDKIMAfterDNSChange(t *testing.T) {
	spike := Spike{
		TenantID: "t1", Domain: "example.com", Provider: "microsoft",
		WindowStart: ts(10, 45), WindowEnd: ts(10, 55),
		DominantReason: ReasonAuth, Rejections: 37,
	}
	changes := []DNSChange{{
		SnapshotID: "snap-2", At: ts(10, 44),
		Findings: []contracts.DNSFinding{{
			Category: "dkim", Severity: "critical", Code: "DKIM_MISSING_SELECTOR",
			Message: "selector2 missing",
		}},
	}}

	c := Correlate(spike, changes, 30*time.Minute)
	if c.Confidence != confHigh {
		t.Errorf("confidence = %v, want high (%v)", c.Confidence, confHigh)
	}
	if c.RelatedChange == nil || c.RelatedChange.SnapshotID != "snap-2" {
		t.Fatalf("expected related change snap-2, got %+v", c.RelatedChange)
	}
	if !strings.Contains(c.RootCause, "authentication") {
		t.Errorf("root cause should mention authentication: %q", c.RootCause)
	}
	if !strings.Contains(c.Remediation, "DKIM selector") {
		t.Errorf("remediation should mention DKIM selector: %q", c.Remediation)
	}
}

func TestCorrelateNoChange(t *testing.T) {
	spike := Spike{
		Domain: "example.com", Provider: "google",
		WindowStart: ts(10, 45), WindowEnd: ts(10, 55),
		DominantReason: ReasonReputation, Rejections: 50,
	}
	c := Correlate(spike, nil, 30*time.Minute)
	if c.Confidence != confLow {
		t.Errorf("confidence = %v, want low", c.Confidence)
	}
	if c.RelatedChange != nil {
		t.Error("expected no related change")
	}
	if !strings.Contains(strings.ToLower(c.Remediation), "reputation") {
		t.Errorf("remediation should address reputation: %q", c.Remediation)
	}
}

func TestCorrelateUnrelatedChange(t *testing.T) {
	spike := Spike{
		Domain: "example.com", Provider: "yahoo",
		WindowStart: ts(10, 45), WindowEnd: ts(10, 55),
		DominantReason: ReasonRateLimit, Rejections: 20,
	}
	changes := []DNSChange{{
		SnapshotID: "snap-x", At: ts(10, 40),
		Findings: []contracts.DNSFinding{{Category: "mx", Severity: "warning", Code: "MX_MISSING", Message: "x"}},
	}}
	c := Correlate(spike, changes, 30*time.Minute)
	if c.Confidence != confMedium {
		t.Errorf("confidence = %v, want medium", c.Confidence)
	}
	if c.RelatedChange == nil {
		t.Error("expected the change to be linked at medium confidence")
	}
}

func TestCorrelateChangeOutsideLookback(t *testing.T) {
	spike := Spike{
		Domain: "example.com", Provider: "microsoft",
		WindowStart: ts(10, 45), WindowEnd: ts(10, 55),
		DominantReason: ReasonAuth, Rejections: 37,
	}
	changes := []DNSChange{{
		SnapshotID: "old", At: ts(9, 0), // ~1h45m before the spike start
		Findings: []contracts.DNSFinding{{Category: "dkim", Severity: "critical", Code: "DKIM_MISSING_SELECTOR", Message: "x"}},
	}}
	c := Correlate(spike, changes, 30*time.Minute)
	if c.RelatedChange != nil {
		t.Error("a change outside the lookback window must not be blamed")
	}
	if c.Confidence != confLow {
		t.Errorf("confidence = %v, want low", c.Confidence)
	}
}
