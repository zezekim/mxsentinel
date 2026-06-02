package correlate

import (
	"fmt"
	"time"

	"github.com/zezekim/mxsentinel/pkg/contracts"
)

// Window is a count of deliveries and rejections over some interval.
type Window struct {
	Total      int
	Rejections int
}

// RejectRate is rejections / total (0 when empty).
func (w Window) RejectRate() float64 {
	if w.Total == 0 {
		return 0
	}
	return float64(w.Rejections) / float64(w.Total)
}

// SpikeConfig tunes spike detection.
type SpikeConfig struct {
	MinRejections      int     // floor to ignore low-volume noise
	RejectRate         float64 // recent reject rate must exceed this absolute threshold
	BaselineMultiplier float64 // recent rate must be >= this * baseline rate
}

// DefaultSpikeConfig is a sensible starting point.
func DefaultSpikeConfig() SpikeConfig {
	return SpikeConfig{MinRejections: 5, RejectRate: 0.30, BaselineMultiplier: 3.0}
}

// SpikeResult reports whether a rejection spike is present and its severity score
// (recent rate relative to baseline; >=1 means at/above baseline).
type SpikeResult struct {
	IsSpike bool
	Score   float64
}

// DetectSpike decides whether the recent window is an anomalous rejection spike versus a
// baseline window.
func DetectSpike(recent, baseline Window, cfg SpikeConfig) SpikeResult {
	if recent.Rejections < cfg.MinRejections {
		return SpikeResult{}
	}
	rr := recent.RejectRate()
	if rr < cfg.RejectRate {
		return SpikeResult{}
	}
	br := baseline.RejectRate()
	if br <= 0 {
		// No baseline failures: an elevated rate with enough volume is itself a spike.
		return SpikeResult{IsSpike: true, Score: rr / 0.01}
	}
	score := rr / br
	if score >= cfg.BaselineMultiplier {
		return SpikeResult{IsSpike: true, Score: score}
	}
	return SpikeResult{Score: score}
}

// DNSChange is a DNS snapshot change observed for a domain (with the findings it carried).
type DNSChange struct {
	SnapshotID string
	At         time.Time
	Findings   []contracts.DNSFinding
}

// Spike summarizes a detected rejection spike to be correlated.
type Spike struct {
	TenantID       string
	Domain         string
	Provider       string
	WindowStart    time.Time
	WindowEnd      time.Time
	DominantReason ReasonCategory
	Rejections     int
}

// Correlation is the engine's output: a root-cause hypothesis for a spike, optionally
// blamed on a preceding DNS change.
type Correlation struct {
	Spike         Spike
	RootCause     string
	Confidence    float64 // 0..1
	RelatedChange *DNSChange
	Remediation   string
}

const (
	confHigh   = 0.85
	confMedium = 0.5
	confLow    = 0.3
)

// authCategories are the DNS finding categories whose changes plausibly cause auth-class
// rejections.
var authCategories = []string{"spf", "dkim", "dmarc"}

// Correlate links a rejection spike to the most recent DNS change that preceded it (within
// lookback of the spike's start) and produces a root-cause hypothesis. With no relevant
// change it falls back to a reason-based explanation.
func Correlate(spike Spike, recentChanges []DNSChange, lookback time.Duration) Correlation {
	c := Correlation{Spike: spike}

	change := mostRecentChangeBefore(recentChanges, spike.WindowStart.Add(-lookback), spike.WindowEnd)

	if change == nil {
		c.Confidence = confLow
		c.RootCause = fmt.Sprintf("Sustained %s failures to %s for %s with no recent DNS change.",
			reasonLabel(spike.DominantReason), providerLabel(spike.Provider), spike.Domain)
		c.Remediation = remediationForReason(spike.DominantReason)
		return c
	}

	c.RelatedChange = change
	if spike.DominantReason == ReasonAuth {
		if f := mostSevereInCategories(change, authCategories...); f != nil {
			c.Confidence = confHigh
			c.RootCause = fmt.Sprintf(
				"DNS change at %s introduced an authentication problem (%s: %s); %s began rejecting %s for authentication shortly after.",
				change.At.UTC().Format(time.RFC3339), f.Category, f.Code, providerLabel(spike.Provider), spike.Domain)
			c.Remediation = remediationForFinding(f.Code)
			return c
		}
	}

	// A change happened just before the spike but doesn't obviously match the reason.
	c.Confidence = confMedium
	c.RootCause = fmt.Sprintf(
		"DNS for %s changed at %s, shortly before %s failures to %s began — likely related.",
		spike.Domain, change.At.UTC().Format(time.RFC3339), reasonLabel(spike.DominantReason), providerLabel(spike.Provider))
	if f := firstFinding(change); f != nil {
		c.Remediation = remediationForFinding(f.Code)
	} else {
		c.Remediation = remediationForReason(spike.DominantReason)
	}
	return c
}

// mostRecentChangeBefore returns the latest change with At in [earliest, latest], or nil.
func mostRecentChangeBefore(changes []DNSChange, earliest, latest time.Time) *DNSChange {
	var best *DNSChange
	for i := range changes {
		ch := &changes[i]
		if ch.At.Before(earliest) || ch.At.After(latest) {
			continue
		}
		if best == nil || ch.At.After(best.At) {
			best = ch
		}
	}
	return best
}

func mostSevereInCategories(change *DNSChange, cats ...string) *contracts.DNSFinding {
	rank := map[string]int{"info": 1, "warning": 2, "critical": 3}
	var best *contracts.DNSFinding
	for i := range change.Findings {
		f := &change.Findings[i]
		if !inSlice(f.Category, cats) {
			continue
		}
		if best == nil || rank[f.Severity] > rank[best.Severity] {
			best = f
		}
	}
	return best
}

func firstFinding(change *DNSChange) *contracts.DNSFinding {
	if len(change.Findings) == 0 {
		return nil
	}
	return &change.Findings[0]
}

func inSlice(v string, xs []string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func remediationForFinding(code string) string {
	switch code {
	case "DKIM_MISSING_SELECTOR":
		return "Restore the missing DKIM selector DNS record."
	case "DKIM_REVOKED":
		return "Republish the DKIM public key (p=) for the selector."
	case "DKIM_WEAK_KEY":
		return "Rotate the DKIM key to a 2048-bit RSA key."
	case "SPF_LOOKUP_LIMIT":
		return "Reduce the SPF record to 10 or fewer DNS lookups (flatten includes)."
	case "SPF_MISSING":
		return "Publish an SPF record authorizing your sending hosts."
	case "SPF_MULTIPLE_RECORDS":
		return "Merge into a single SPF TXT record."
	case "DMARC_MISSING":
		return "Publish a DMARC record at _dmarc."
	case "DMARC_POLICY_NONE":
		return "Advance the DMARC policy to quarantine or reject after monitoring."
	default:
		return "Review the DNS finding: " + code + "."
	}
}

func remediationForReason(r ReasonCategory) string {
	switch r {
	case ReasonReputation:
		return "Investigate sending-IP/domain reputation; consider IP warmup or pausing the affected stream."
	case ReasonBlocklist:
		return "Check RBL listings for the relay IP and request delisting."
	case ReasonAuth:
		return "Verify SPF, DKIM, and DMARC alignment for the sending domain."
	case ReasonRateLimit:
		return "Reduce send rate to this provider and spread volume across the IP pool."
	case ReasonSpamContent:
		return "Review message content and recipient engagement; reduce spammy patterns."
	case ReasonGreylist:
		return "Transient greylisting — ensure the queue honors retries."
	case ReasonUserUnknown:
		return "Clean the recipient list; remove invalid addresses."
	case ReasonMailboxFull:
		return "Recipient-side over quota — transient; retry later."
	case ReasonTLS:
		return "Check outbound TLS configuration and certificate validity."
	default:
		return "Review provider responses for this domain."
	}
}

func reasonLabel(r ReasonCategory) string {
	if r == "" {
		return "delivery"
	}
	return string(r)
}

func providerLabel(p string) string {
	if p == "" {
		return "the provider"
	}
	return p
}
