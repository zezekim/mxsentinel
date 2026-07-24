package report

import (
	"context"
	"fmt"
	"time"

	chstore "github.com/zezekim/mxsentinel/internal/store/clickhouse"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
)

// DomainRow is one sending domain's counts in the summary (for the top-domains table).
type DomainRow struct {
	Domain string `json:"domain"`
	Counts
}

// SummaryReport is a tenant-wide deliverability overview for a period: overall counts, a
// per-receiver-provider breakdown, and the highest-volume sending domains (with their rates).
// It's designed to drop straight into an admin dashboard/report (e.g. a WHMCS report page).
type SummaryReport struct {
	PeriodStart time.Time     `json:"period_start"`
	PeriodEnd   time.Time     `json:"period_end"`
	Overall     Counts        `json:"overall"`
	Providers   []ProviderRow `json:"providers"`
	TopDomains  []DomainRow   `json:"top_domains"`
}

// BuildSummary assembles the tenant-wide summary. topN caps the top-domains table (<=0 → 15).
// The pgstore arg is currently unused (reserved for future score enrichment) but kept for
// signature symmetry with Build.
func BuildSummary(ctx context.Context, ch *chstore.Store, _ *pgstore.Store, tenantID string, since, until time.Time, topN int) (SummaryReport, error) {
	if topN <= 0 {
		topN = 15
	}
	r := SummaryReport{PeriodStart: since, PeriodEnd: until}

	// Per-provider (and, summed, the overall totals).
	pv, err := ch.DeliverabilityByProvider(ctx, tenantID, since, until)
	if err != nil {
		return SummaryReport{}, fmt.Errorf("deliverability by provider: %w", err)
	}
	for _, p := range pv {
		name := p.Provider
		if name == "" {
			name = "other"
		}
		r.Providers = append(r.Providers, ProviderRow{
			Provider: name,
			Counts:   Counts{Delivered: p.Delivered, Deferred: p.Deferred, Bounced: p.Bounced, Rejected: p.Rejected, Total: p.Total},
		})
		r.Overall.Delivered += p.Delivered
		r.Overall.Deferred += p.Deferred
		r.Overall.Bounced += p.Bounced
		r.Overall.Rejected += p.Rejected
		r.Overall.Total += p.Total
	}

	// Top sending domains by volume (MetricsByDomain returns rows ordered by total DESC).
	dm, err := ch.MetricsByDomain(ctx, tenantID, nil, since, until)
	if err != nil {
		return SummaryReport{}, fmt.Errorf("metrics by domain: %w", err)
	}
	for i, d := range dm {
		if i >= topN {
			break
		}
		r.TopDomains = append(r.TopDomains, DomainRow{
			Domain: d.FromDomain,
			Counts: Counts{Delivered: d.Delivered, Deferred: d.Deferred, Bounced: d.Bounced, Rejected: d.Rejected, Total: d.Total},
		})
	}

	return r, nil
}
