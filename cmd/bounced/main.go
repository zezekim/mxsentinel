// Command bounced is the bounce-classification + suppression worker (the Remediate step).
// On an interval it re-reads recent bounced/rejected telemetry from ClickHouse, runs the
// pure internal/bounce classifier over each row, and:
//
//   - recomputes per-day, per-domain, per-category classified counts into the Postgres
//     bounce_rollup table (authoritative recompute over a fixed lookback window — idempotent);
//   - auto-populates the per-tenant suppression list from terminal bounces (hard bounces,
//     invalid recipients, spam blocks) keyed by the recipient HASH only.
//
// Per-domain bounce RATES are maintained separately and automatically by the ClickHouse
// bounce_daily materialized view, so this daemon does not write any ClickHouse rows.
//
// It reads telemetry via the store API only (no direct cross-service DB coupling beyond the
// shared stores) and holds no message bodies — recipient hashes and codes only.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/zezekim/mxsentinel/internal/bounce"
	"github.com/zezekim/mxsentinel/internal/config"
	"github.com/zezekim/mxsentinel/internal/obs"
	chstore "github.com/zezekim/mxsentinel/internal/store/clickhouse"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
)

func main() {
	intervalFlag := flag.Duration("interval", 0, "override scan interval (default from MXS_BOUNCE_INTERVAL or 5m)")
	flag.Parse()

	if err := run(*intervalFlag); err != nil {
		fmt.Fprintln(os.Stderr, "bounced:", err)
		os.Exit(1)
	}
}

func run(intervalOverride time.Duration) error {
	cfg, err := config.Load(os.Getenv("MXS_CONFIG"))
	if err != nil {
		return err
	}
	bcfg := bounce.LoadConfig()
	if intervalOverride > 0 {
		bcfg.Interval = intervalOverride
	}
	log := obs.NewLogger("bounced", cfg.LogLevel)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	metrics := obs.NewMetrics()
	srv := obs.NewServer(cfg.HTTPAddr, metrics, log)
	srv.Start()
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()

	pg, err := pgstore.New(ctx, cfg.Postgres)
	if err != nil {
		return err
	}
	defer pg.Close()

	// Overlay dashboard-managed tuning (Postgres) over the env-loaded config. Precedence is
	// DASHBOARD > env > default: a value set via the Settings page wins, an unset (zero) value
	// leaves the env/default in place, and a DB read error is treated as "unset" so we never
	// blank a working env-configured scanner. Scoped to RELAY_TENANT_ID (the single tenant this
	// relay's daemons run for). The explicit -interval flag (below) is re-applied afterwards so a
	// deliberate CLI override still wins over the dashboard for one-off debugging.
	if tenantID := strings.TrimSpace(os.Getenv("RELAY_TENANT_ID")); tenantID != "" {
		if t, err := pg.GetAbuseTuning(ctx, tenantID); err != nil {
			log.Warn("abuse tuning: read failed; using env/default", "tenant_id", tenantID, "err", err)
		} else {
			applyBounceTuning(&bcfg, t.Bounce, log)
		}
	}
	if intervalOverride > 0 {
		bcfg.Interval = intervalOverride
	}

	var ch *chstore.Store
	if err := obs.Retry(ctx, log, "clickhouse", 20, 3*time.Second, func() (e error) {
		ch, e = chstore.New(ctx, cfg.ClickHouse)
		return
	}); err != nil {
		return err
	}
	defer ch.Close()

	w := &worker{log: log, pg: pg, ch: ch, cfg: bcfg}
	srv.SetReady(true)
	log.Info("bounced started",
		"interval", bcfg.Interval.String(), "lookback", bcfg.Lookback.String(), "max_rows", bcfg.MaxRows)

	ticker := time.NewTicker(bcfg.Interval)
	defer ticker.Stop()
	w.scan(ctx)
	for {
		select {
		case <-ctx.Done():
			log.Info("bounced shutting down")
			return nil
		case <-ticker.C:
			w.scan(ctx)
		}
	}
}

type worker struct {
	log *slog.Logger
	pg  *pgstore.Store
	ch  *chstore.Store
	cfg bounce.Config
}

// rollupKey identifies a (tenant, day, from_domain, category) rollup cell.
type rollupKey struct {
	tenant   string
	day      time.Time // truncated to date (UTC midnight)
	domain   string
	category string
}

// suppressCandidate is the strongest suppression decision seen for a (tenant, hash) pair in
// one scan.
type suppressCandidate struct {
	tenant   string
	hash     string
	category string
	decision bounce.SuppressionDecision
}

func (w *worker) scan(ctx context.Context) {
	since := time.Now().Add(-w.cfg.Lookback)
	rows, err := w.ch.RecentBouncesAllTenants(ctx, since, w.cfg.MaxRows)
	if err != nil {
		w.log.Error("read recent bounces", "err", err)
		return
	}

	rollups := map[rollupKey]int{}
	suppress := map[string]suppressCandidate{} // key: tenant|hash

	for _, row := range rows {
		cat := bounce.Classify(int(row.SMTPCode), row.EnhancedStatus, row.ResponseText)

		day := row.EventTime.UTC().Truncate(24 * time.Hour)
		rollups[rollupKey{tenant: row.TenantID, day: day, domain: row.FromDomain, category: string(cat)}]++

		d := bounce.SuppressionFor(cat)
		if !d.Suppress || row.RecipientHash == "" {
			continue
		}
		k := row.TenantID + "|" + row.RecipientHash
		if prev, ok := suppress[k]; !ok || d.Priority > prev.decision.Priority {
			suppress[k] = suppressCandidate{tenant: row.TenantID, hash: row.RecipientHash, category: string(cat), decision: d}
		}
	}

	w.writeRollups(ctx, rollups)
	added := w.writeSuppression(ctx, suppress)

	w.log.Info("bounce scan complete",
		"rows", len(rows), "rollup_cells", len(rollups),
		"suppress_candidates", len(suppress), "suppress_added", added)
}

func (w *worker) writeRollups(ctx context.Context, rollups map[rollupKey]int) {
	// Deterministic order keeps logs/debugging sane; not required for correctness.
	keys := make([]rollupKey, 0, len(rollups))
	for k := range rollups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].tenant != keys[j].tenant {
			return keys[i].tenant < keys[j].tenant
		}
		if !keys[i].day.Equal(keys[j].day) {
			return keys[i].day.Before(keys[j].day)
		}
		if keys[i].domain != keys[j].domain {
			return keys[i].domain < keys[j].domain
		}
		return keys[i].category < keys[j].category
	})
	for _, k := range keys {
		if ctx.Err() != nil {
			return
		}
		err := w.pg.UpsertBounceRollup(ctx, pgstore.BounceRollupRow{
			TenantID: k.tenant, Day: k.day, FromDomain: k.domain, Category: k.category, Count: rollups[k],
		})
		if err != nil {
			w.log.Error("upsert bounce rollup", "tenant_id", k.tenant, "day", k.day.Format("2006-01-02"), "err", err)
		}
	}
}

func (w *worker) writeSuppression(ctx context.Context, suppress map[string]suppressCandidate) int {
	now := time.Now()
	added := 0
	for _, c := range suppress {
		if ctx.Err() != nil {
			return added
		}
		inserted, err := w.pg.UpsertSuppression(ctx, c.tenant, c.hash, c.decision.Reason, c.category, bounce.SourceBounce, c.decision.ExpiryFor(now))
		if err != nil {
			w.log.Error("upsert suppression", "tenant_id", c.tenant, "err", err)
			continue
		}
		if inserted {
			added++
		}
	}
	return added
}

// applyBounceTuning overlays non-zero dashboard-managed values onto the bounced config. A zero
// value means "unset" — left untouched so the env/default stands. Interval/Lookback arrive as
// integer seconds. It logs how many fields it changed for auditability at startup.
func applyBounceTuning(c *bounce.Config, t pgstore.BounceTuning, log *slog.Logger) {
	n := 0
	if t.IntervalSecs > 0 {
		c.Interval = time.Duration(t.IntervalSecs) * time.Second
		n++
	}
	if t.LookbackSecs > 0 {
		c.Lookback = time.Duration(t.LookbackSecs) * time.Second
		n++
	}
	if t.MaxRows > 0 {
		c.MaxRows = t.MaxRows
		n++
	}
	if n > 0 {
		log.Info("applied dashboard abuse tuning (bounce)", "fields", n)
	}
}
