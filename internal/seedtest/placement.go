// Package seedtest implements inbox-placement / seed-list testing: sending uniquely-tagged
// synthetic probe messages to a configured list of seed mailboxes across providers (Gmail,
// Outlook, Yahoo, ...) and measuring inbox vs. spam vs. missing placement per provider and
// per sending IP. This is the GlockApps/Litmus-style capability.
//
// The package is split into pure, table-driven-tested logic (placement aggregation and
// header/auth parsing, this file) and network seams behind interfaces (Sender in sender.go,
// Collector in collector.go) so the SMTP send and IMAP fetch can be faked in tests — no live
// network is touched by `make test`.
//
// Privacy boundary: probe messages are synthetic test content that MX Sentinel generates, so
// they are safe to send and read back. The collector only ever matches our own probes (by
// their unique tag); no real user mail is ingested, stored, or logged.
package seedtest

import (
	"regexp"
	"strings"
)

// Placement is the bucket a probe landed in for a single seed.
type Placement string

const (
	PlacementUnknown Placement = "unknown" // not yet observed
	PlacementInbox   Placement = "inbox"
	PlacementSpam    Placement = "spam"
	PlacementMissing Placement = "missing" // sent but never found within the collection window
)

// Result-status values (mirrors the seed_results.status column).
const (
	StatusPending = "pending" // created, probe not yet sent
	StatusSent    = "sent"    // probe delivered to the seed; awaiting collection
	StatusInbox   = "inbox"
	StatusSpam    = "spam"
	StatusMissing = "missing"
	StatusError   = "error"
)

// Run-status values (mirrors the seed_runs.status column).
const (
	RunPending    = "pending"
	RunSending    = "sending"
	RunCollecting = "collecting"
	RunCompleted  = "completed"
	RunFailed     = "failed"
)

// Providers recognized for classification/tagging. Unknown providers fall back to "other".
const (
	ProviderGmail   = "gmail"
	ProviderOutlook = "outlook"
	ProviderYahoo   = "yahoo"
	ProviderOther   = "other"
)

// AuthResults holds the SPF/DKIM/DMARC verdicts parsed from a delivered probe's
// Authentication-Results header. A nil pointer means "not present / unknown"; a non-nil
// pointer is true only when the mechanism explicitly reported "pass".
type AuthResults struct {
	SPF   *bool
	DKIM  *bool
	DMARC *bool
}

// Result is one seed's outcome within a run, in the shape the aggregator consumes. It is a
// projection of the seed_results row (see internal/store/postgres/seed_testing.go).
type Result struct {
	Address   string
	Provider  string
	Status    string
	Placement Placement
	SPFPass   *bool
	DKIMPass  *bool
	DMARCPass *bool
}

// ProviderSummary is the aggregated placement for a single provider within a run.
type ProviderSummary struct {
	Provider      string  `json:"provider"`
	Total         int     `json:"total"`
	Inbox         int     `json:"inbox"`
	Spam          int     `json:"spam"`
	Missing       int     `json:"missing"`
	Pending       int     `json:"pending"`
	InboxRate     float64 `json:"inbox_rate"`    // inbox / resolved
	SpamRate      float64 `json:"spam_rate"`     // spam / resolved
	MissingRate   float64 `json:"missing_rate"`  // missing / resolved
	SPFPassRate   float64 `json:"spf_pass_rate"` // over results with a known SPF verdict
	DKIMPassRate  float64 `json:"dkim_pass_rate"`
	DMARCPassRate float64 `json:"dmarc_pass_rate"`
}

// Summary is the full placement rollup for a run: an overall row plus per-provider rows.
type Summary struct {
	Overall   ProviderSummary   `json:"overall"`
	Providers []ProviderSummary `json:"providers"`
}

// Summarize aggregates per-seed results into an overall + per-provider placement summary.
// "Resolved" means a terminal placement (inbox/spam/missing); pending seeds are counted but
// excluded from the rate denominators so rates aren't diluted while a run is still collecting.
// It is deterministic: provider rows are ordered by descending total, then provider name.
func Summarize(results []Result) Summary {
	byProvider := map[string]*ProviderSummary{}
	order := []string{}
	overall := &ProviderSummary{Provider: "overall"}

	// auth counters must track denominators separately from the placement counters, so keep
	// them in a parallel struct keyed the same way.
	type authAcc struct {
		spfKnown, spfPass     int
		dkimKnown, dkimPass   int
		dmarcKnown, dmarcPass int
	}
	auth := map[string]*authAcc{}
	overallAuth := &authAcc{}

	get := func(p string) (*ProviderSummary, *authAcc) {
		ps, ok := byProvider[p]
		if !ok {
			ps = &ProviderSummary{Provider: p}
			byProvider[p] = ps
			auth[p] = &authAcc{}
			order = append(order, p)
		}
		return ps, auth[p]
	}

	tallyAuth := func(a *authAcc, v *bool, whichKnown, whichPass *int) {
		_ = a
		if v == nil {
			return
		}
		*whichKnown++
		if *v {
			*whichPass++
		}
	}

	for _, r := range results {
		provider := NormalizeProvider(r.Provider)
		ps, a := get(provider)

		place := r.Placement
		if place == "" {
			place = PlacementUnknown
		}

		for _, target := range []*ProviderSummary{ps, overall} {
			target.Total++
			switch place {
			case PlacementInbox:
				target.Inbox++
			case PlacementSpam:
				target.Spam++
			case PlacementMissing:
				target.Missing++
			default:
				target.Pending++
			}
		}
		for _, acc := range []*authAcc{a, overallAuth} {
			tallyAuth(acc, r.SPFPass, &acc.spfKnown, &acc.spfPass)
			tallyAuth(acc, r.DKIMPass, &acc.dkimKnown, &acc.dkimPass)
			tallyAuth(acc, r.DMARCPass, &acc.dmarcKnown, &acc.dmarcPass)
		}
	}

	finalize := func(ps *ProviderSummary, a *authAcc) {
		resolved := ps.Inbox + ps.Spam + ps.Missing
		if resolved > 0 {
			ps.InboxRate = ratio(ps.Inbox, resolved)
			ps.SpamRate = ratio(ps.Spam, resolved)
			ps.MissingRate = ratio(ps.Missing, resolved)
		}
		ps.SPFPassRate = ratio(a.spfPass, a.spfKnown)
		ps.DKIMPassRate = ratio(a.dkimPass, a.dkimKnown)
		ps.DMARCPassRate = ratio(a.dmarcPass, a.dmarcKnown)
	}

	providers := make([]ProviderSummary, 0, len(order))
	for _, p := range order {
		finalize(byProvider[p], auth[p])
		providers = append(providers, *byProvider[p])
	}
	// Deterministic order: highest total first, ties broken by provider name.
	sortProviderSummaries(providers)
	finalize(overall, overallAuth)

	return Summary{Overall: *overall, Providers: providers}
}

func ratio(n, d int) float64 {
	if d <= 0 {
		return 0
	}
	return float64(n) / float64(d)
}

func sortProviderSummaries(ps []ProviderSummary) {
	// small n (a handful of providers): simple insertion sort keeps it dependency-free and
	// stable-enough for the deterministic ordering we document.
	for i := 1; i < len(ps); i++ {
		j := i
		for j > 0 && less(ps[j], ps[j-1]) {
			ps[j], ps[j-1] = ps[j-1], ps[j]
			j--
		}
	}
}

func less(a, b ProviderSummary) bool {
	if a.Total != b.Total {
		return a.Total > b.Total
	}
	return a.Provider < b.Provider
}

// NormalizeProvider maps a free-form provider label (or an email domain) to one of the
// recognized provider buckets. It accepts both explicit labels ("gmail") and hostnames
// ("mail.google.com"), so it can classify either a seed's declared provider or its address
// domain.
func NormalizeProvider(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ProviderOther
	}
	switch s {
	case ProviderGmail, ProviderOutlook, ProviderYahoo, ProviderOther:
		return s
	}
	switch {
	case strings.Contains(s, "gmail") || strings.Contains(s, "google") || strings.Contains(s, "googlemail"):
		return ProviderGmail
	case strings.Contains(s, "outlook") || strings.Contains(s, "hotmail") || strings.Contains(s, "live.") || strings.Contains(s, "office365") || strings.Contains(s, "microsoft"):
		return ProviderOutlook
	case strings.Contains(s, "yahoo") || strings.Contains(s, "ymail") || strings.Contains(s, "aol"):
		return ProviderYahoo
	default:
		return ProviderOther
	}
}

// ProviderForAddress classifies a seed address by the domain part of its email.
func ProviderForAddress(address string) string {
	at := strings.LastIndex(address, "@")
	if at < 0 || at == len(address)-1 {
		return ProviderOther
	}
	return NormalizeProvider(address[at+1:])
}

// mechRe matches "mech=result" tokens (e.g. "spf=pass", "dkim=fail") inside an
// Authentication-Results header, tolerant of surrounding whitespace.
var mechRe = regexp.MustCompile(`(?i)\b(spf|dkim|dmarc)\s*=\s*([a-z]+)`)

// ParseAuthResults extracts SPF/DKIM/DMARC verdicts from one or more Authentication-Results
// header values (RFC 8601). Each mechanism resolves to:
//   - non-nil true  when the newest occurrence reports "pass"
//   - non-nil false when it reports any other verdict (fail, softfail, none, neutral, ...)
//   - nil           when the mechanism does not appear at all
//
// When a mechanism appears more than once (multiple relays stamped their own headers), the
// last occurrence wins, matching how the receiving MTA closest to the mailbox is authoritative.
func ParseAuthResults(headerValues ...string) AuthResults {
	var out AuthResults
	for _, h := range headerValues {
		for _, m := range mechRe.FindAllStringSubmatch(h, -1) {
			mech := strings.ToLower(m[1])
			pass := strings.EqualFold(m[2], "pass")
			b := pass
			switch mech {
			case "spf":
				out.SPF = boolPtr(b, out.SPF)
			case "dkim":
				out.DKIM = boolPtr(b, out.DKIM)
			case "dmarc":
				out.DMARC = boolPtr(b, out.DMARC)
			}
		}
	}
	return out
}

// boolPtr returns a pointer to v; prev is ignored except to make the "last occurrence wins"
// intent explicit at call sites.
func boolPtr(v bool, _ *bool) *bool {
	b := v
	return &b
}

// ClassifyMailbox maps an IMAP mailbox/folder name to a Placement. Junk/spam folder names
// across providers (Spam, Junk, "Bulk Mail", "[Gmail]/Spam", ...) map to PlacementSpam;
// everything else (INBOX and provider category tabs) maps to PlacementInbox.
func ClassifyMailbox(mailbox string) Placement {
	m := strings.ToLower(mailbox)
	switch {
	case strings.Contains(m, "spam"),
		strings.Contains(m, "junk"),
		strings.Contains(m, "bulk"):
		return PlacementSpam
	default:
		return PlacementInbox
	}
}
