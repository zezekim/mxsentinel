// Package report builds a per-domain deliverability report by aggregating the SMTP outcome
// counts, per-provider breakdown, health score, and seed-list inbox placement that MX Sentinel
// already collects. The same DomainReport feeds the dashboard's copy-to-clipboard report view
// and the WHMCS per-client stats block pushed by cpaneld.
package report

import (
	"context"
	"fmt"
	"strings"
	"time"

	chstore "github.com/zezekim/mxsentinel/internal/store/clickhouse"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
)

// Counts is the standard SMTP-outcome tally with derived rates.
type Counts struct {
	Delivered uint64 `json:"delivered"`
	Deferred  uint64 `json:"deferred"`
	Bounced   uint64 `json:"bounced"`
	Rejected  uint64 `json:"rejected"`
	Total     uint64 `json:"total"`
}

func pct(n, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(n) / float64(total) * 100
}

// DeliveredPct etc. are the outcome shares of Total (0 when no volume).
func (c Counts) DeliveredPct() float64 { return pct(c.Delivered, c.Total) }
func (c Counts) BouncedPct() float64   { return pct(c.Bounced, c.Total) }
func (c Counts) DeferredPct() float64  { return pct(c.Deferred, c.Total) }
func (c Counts) RejectedPct() float64  { return pct(c.Rejected, c.Total) }

// ProviderRow is one receiver provider's share of a domain's mail.
type ProviderRow struct {
	Provider string `json:"provider"`
	Counts
}

// ScoreInfo is the domain's latest deliverability health score.
type ScoreInfo struct {
	Score      float64   `json:"score"`
	Grade      string    `json:"grade"`
	Coverage   float64   `json:"coverage"`
	ComputedAt time.Time `json:"computed_at"`
}

// PlacementRow is seed-list inbox placement for one provider (relay-wide, not per-domain —
// seed tests aren't per-customer-domain, so it's presented as an overall signal).
type PlacementRow struct {
	Provider string `json:"provider"`
	Inbox    uint64 `json:"inbox"`
	Spam     uint64 `json:"spam"`
	Missing  uint64 `json:"missing"`
	Total    uint64 `json:"total"`
}

func (p PlacementRow) InboxPct() float64 { return pct(p.Inbox, p.Total) }

// DomainReport is the full assembled report for one sending domain over a period.
type DomainReport struct {
	Domain      string         `json:"domain"`
	PeriodStart time.Time      `json:"period_start"`
	PeriodEnd   time.Time      `json:"period_end"`
	Core        Counts         `json:"core"`
	Providers   []ProviderRow  `json:"providers"`
	Score       *ScoreInfo     `json:"score,omitempty"`
	Placement   []PlacementRow `json:"placement,omitempty"`
}

// Build assembles a DomainReport from the stores. Sections that have no data (no score yet, no
// seed tests) are simply omitted — the report degrades gracefully rather than erroring.
func Build(ctx context.Context, ch *chstore.Store, pg *pgstore.Store, tenantID, domain string, since, until time.Time) (DomainReport, error) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	r := DomainReport{Domain: domain, PeriodStart: since, PeriodEnd: until}

	// Core outcome counts (from_domain).
	metrics, err := ch.MetricsByDomain(ctx, tenantID, []string{domain}, since, until)
	if err != nil {
		return DomainReport{}, fmt.Errorf("core metrics: %w", err)
	}
	for _, m := range metrics {
		if strings.EqualFold(m.FromDomain, domain) {
			r.Core = Counts{Delivered: m.Delivered, Deferred: m.Deferred, Bounced: m.Bounced, Rejected: m.Rejected, Total: m.Total}
			break
		}
	}

	// Per-provider breakdown.
	if pv, perr := ch.ProviderBreakdownForFromDomain(ctx, tenantID, domain, since, until); perr == nil {
		for _, p := range pv {
			r.Providers = append(r.Providers, ProviderRow{
				Provider: p.Provider,
				Counts:   Counts{Delivered: p.Delivered, Deferred: p.Deferred, Bounced: p.Bounced, Rejected: p.Rejected, Total: p.Total},
			})
		}
	}

	// Latest health score for this domain (match by name; omitted if none).
	if scores, serr := pg.LatestHealthScores(ctx, tenantID); serr == nil {
		for _, sc := range scores {
			if strings.EqualFold(sc.DomainName, domain) && sc.HasData {
				r.Score = &ScoreInfo{Score: sc.Score, Grade: sc.Grade, Coverage: sc.Coverage, ComputedAt: sc.ComputedAt}
				break
			}
		}
	}

	// Seed-list inbox placement (relay-wide, best-effort).
	if pl, plerr := ch.PlacementByProvider(ctx, tenantID, since); plerr == nil {
		for _, t := range pl {
			if t.Total == 0 {
				continue
			}
			r.Placement = append(r.Placement, PlacementRow{Provider: t.Provider, Inbox: t.Inbox, Spam: t.Spam, Missing: t.Missing, Total: t.Total})
		}
	}

	return r, nil
}
