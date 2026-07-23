package healthscore

import (
	"context"
	"fmt"
	"time"

	"github.com/zezekim/mxsentinel/internal/anomaly"
	"github.com/zezekim/mxsentinel/internal/fbl"
	"github.com/zezekim/mxsentinel/internal/rbl"
	chstore "github.com/zezekim/mxsentinel/internal/store/clickhouse"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
)

// DefaultWindow is the look-back over which telemetry-derived components (bounce ratio, DMARC
// alignment, anomaly recency) are aggregated.
const DefaultWindow = 7 * 24 * time.Hour

// Collector assembles Inputs for the scorer by reading — READ-ONLY — the stores that other
// subsystems already populate. It owns no schema of its own and adds nothing to the shared
// store packages; it reuses their exported query methods (rbl, fbl, anomaly, ClickHouse
// analytics). Where a per-domain aggregate simply is not available from an existing method the
// signal degrades to a tenant/relay-level one (documented in docs/health-score.md).
type Collector struct {
	pg      *pgstore.Store
	ch      *chstore.Store
	rbl     *rbl.Store
	anomaly *anomaly.Store
	fbl     *fbl.Store
	window  time.Duration
}

// NewCollector builds a Collector over the shared Postgres and ClickHouse stores. ch may be
// nil (e.g. in environments without ClickHouse) — the DMARC and bounce components then simply
// degrade to absent/neutral.
func NewCollector(pg *pgstore.Store, ch *chstore.Store) *Collector {
	return &Collector{
		pg:      pg,
		ch:      ch,
		rbl:     rbl.NewStore(pg),
		anomaly: anomaly.NewStore(pg.Pool),
		fbl:     fbl.NewStore(pg.Pool),
		window:  DefaultWindow,
	}
}

// WithWindow overrides the aggregation window (chainable).
func (c *Collector) WithWindow(d time.Duration) *Collector {
	if d > 0 {
		c.window = d
	}
	return c
}

// Shared holds tenant/relay-level signals computed once per pass and reused across every
// domain of a tenant: the relay's blocklist posture (global), the tenant's bounce ratio, and
// the per-sender complaint/Postmaster reputation index.
type Shared struct {
	Bounce      *BounceInput
	Blocklist   *BlocklistInput
	reputations map[string]fbl.ReputationRow // keyed by sending domain
	movers      map[string]anomaly.MoverRow  // keyed by sending domain
}

// CollectShared computes the tenant/relay-level signals once. now is passed explicitly so the
// caller (and tests) control the clock.
func (c *Collector) CollectShared(ctx context.Context, tenantID string, now time.Time) (Shared, error) {
	sh := Shared{
		reputations: map[string]fbl.ReputationRow{},
		movers:      map[string]anomaly.MoverRow{},
	}
	since := now.Add(-c.window)

	// Relay blocklist posture (global infra state from rbld/repd).
	if rows, err := c.rbl.List(ctx); err == nil {
		total := map[string]bool{}
		listed := map[string]bool{}
		for _, r := range rows {
			total[r.IP] = true
			if r.Listed {
				listed[r.IP] = true
			}
		}
		if len(total) > 0 {
			sh.Blocklist = &BlocklistInput{TotalIPs: len(total), ListedIPs: len(listed)}
		}
	}

	// Tenant bounce ratio from ClickHouse smtp_events (summed across providers). No per-domain
	// aggregate method exists, so this is a tenant-level signal shared across the tenant's
	// domains — documented graceful degradation.
	if c.ch != nil {
		if provs, err := c.ch.DeliverabilityByProvider(ctx, tenantID, since, now); err == nil {
			var b BounceInput
			for _, p := range provs {
				b.Total += p.Total
				b.Bounced += p.Bounced
				b.Rejected += p.Rejected
				b.Deferred += p.Deferred
			}
			if b.Total > 0 {
				sh.Bounce = &b
			}
		}
	}

	// Per-sender complaint + Postmaster reputation index (global by domain name).
	if rows, err := c.fbl.ListReputation(ctx, 1000); err == nil {
		for _, r := range rows {
			sh.reputations[r.Domain] = r
		}
	}

	// Recent volume-anomaly trips per sending domain within the window.
	if movers, err := c.anomaly.TopMovers(ctx, tenantID, c.window, 100); err == nil {
		for _, m := range movers {
			sh.movers[m.SenderDomain] = m
		}
	}

	return sh, nil
}

// CollectDomain assembles the full Inputs for a single domain, folding in the shared signals.
func (c *Collector) CollectDomain(ctx context.Context, tenantID, domainName string, sh Shared, now time.Time) (Inputs, error) {
	in := Inputs{
		Bounce:    sh.Bounce,
		Blocklist: sh.Blocklist,
	}
	since := now.Add(-c.window)

	// DMARC alignment for this domain (absent when no aggregate reports in window).
	if c.ch != nil {
		if a, err := c.ch.DMARCAlignmentSummary(ctx, tenantID, domainName, since, now); err == nil && a.Total > 0 {
			in.DMARC = &DMARCInput{Total: a.Total, DKIMAligned: a.DKIMAligned, SPFAligned: a.SPFAligned}
		}
	}

	// Complaints + Postmaster reputation for this sending domain.
	if rep, ok := sh.reputations[domainName]; ok {
		in.Complaints = &ComplaintInput{Complaints24h: rep.Complaints24h}
		if rep.Reputation != "" || rep.SpamRate != nil {
			in.Postmaster = &PostmasterInput{Grade: rep.Reputation, SpamRate: rep.SpamRate}
		}
	}

	// Volume-anomaly state: a recent trip in the window is an active spike.
	if m, ok := sh.movers[domainName]; ok {
		in.Anomaly = &AnomalyInput{ActiveSpike: true, Ratio: m.Ratio}
	} else {
		// No trip recorded in the window means the domain is behaving normally.
		in.Anomaly = &AnomalyInput{ActiveSpike: false}
	}

	return in, nil
}

// ScoreDomain is a convenience that collects shared+domain inputs and computes the score with
// the default weights. Prefer CollectShared + CollectDomain in a loop over many domains.
func (c *Collector) ScoreDomain(ctx context.Context, tenantID, domainName string, now time.Time) (Result, error) {
	sh, err := c.CollectShared(ctx, tenantID, now)
	if err != nil {
		return Result{}, fmt.Errorf("collect shared: %w", err)
	}
	in, err := c.CollectDomain(ctx, tenantID, domainName, sh, now)
	if err != nil {
		return Result{}, fmt.Errorf("collect domain %s: %w", domainName, err)
	}
	return Compute(in, DefaultWeights()), nil
}
