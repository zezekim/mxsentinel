package report

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// commas groups an integer with thousands separators (1234567 -> "1,234,567").
func commas(n uint64) string {
	s := strconv.FormatUint(n, 10)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
		if len(s) > pre {
			b.WriteByte(',')
		}
	}
	for i := pre; i < len(s); i += 3 {
		b.WriteString(s[i : i+3])
		if i+3 < len(s) {
			b.WriteByte(',')
		}
	}
	return b.String()
}

// Text renders the report as a plain-text block suitable for pasting into a report or a WHMCS
// client field. It is deterministic and self-contained (no external formatting).
func (r DomainReport) Text() string {
	var b strings.Builder
	period := fmt.Sprintf("%s to %s",
		r.PeriodStart.UTC().Format("2006-01-02"), r.PeriodEnd.UTC().Format("2006-01-02"))

	fmt.Fprintf(&b, "MX Sentinel — Deliverability Report\n%s · %s\n", r.Domain, period)

	if r.Score != nil {
		fmt.Fprintf(&b, "Health score: %.0f (%s) · coverage %.0f%%\n", r.Score.Score, r.Score.Grade, r.Score.Coverage*100)
	}
	b.WriteByte('\n')

	c := r.Core
	if c.Total == 0 {
		b.WriteString("No mail sent from this domain in the period.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "Sent %s   Delivered %s (%.1f%%)   Bounced %s (%.1f%%)   Deferred %s (%.1f%%)   Rejected %s (%.1f%%)\n",
		commas(c.Total),
		commas(c.Delivered), c.DeliveredPct(),
		commas(c.Bounced), c.BouncedPct(),
		commas(c.Deferred), c.DeferredPct(),
		commas(c.Rejected), c.RejectedPct(),
	)

	if len(r.Providers) > 0 {
		b.WriteString("\nBy provider\n")
		for _, p := range r.Providers {
			fmt.Fprintf(&b, "  %-11s %7s   delivered %s (%.1f%%)   bounced %s (%.1f%%)\n",
				titleProvider(p.Provider), commas(p.Total),
				commas(p.Delivered), p.DeliveredPct(),
				commas(p.Bounced), p.BouncedPct(),
			)
		}
	}

	if len(r.Placement) > 0 {
		b.WriteString("\nInbox placement (seed tests, relay-wide)\n")
		for _, p := range r.Placement {
			fmt.Fprintf(&b, "  %-11s inbox %.0f%%  (%d/%d)\n",
				titleProvider(p.Provider), p.InboxPct(), p.Inbox, p.Total)
		}
	}

	fmt.Fprintf(&b, "\nGenerated %s UTC\n", time.Now().UTC().Format("2006-01-02 15:04"))
	return b.String()
}

func titleProvider(p string) string {
	switch strings.ToLower(p) {
	case "microsoft":
		return "Microsoft"
	case "google":
		return "Google"
	case "yahoo":
		return "Yahoo"
	case "apple":
		return "Apple"
	case "":
		return "Other"
	default:
		return strings.ToUpper(p[:1]) + p[1:]
	}
}
