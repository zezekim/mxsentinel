// Command scored is the Deliverability Health Score snapshotter. On an interval it recomputes
// each monitored domain's composite 0–100 health score — fusing DMARC/auth alignment,
// feedback-loop complaint volume, blocklist/reputation posture, bounce ratio, send-volume
// anomaly state, and Gmail Postmaster reputation — and appends a snapshot to Postgres
// (health_score_snapshots) so the dashboard can render a trend and incident/AI explanations can
// cite the score's drivers.
//
// It reads existing stores READ-ONLY via internal/healthscore's collector; it owns only the
// snapshot table. Scoring is pure (internal/healthscore.Compute) and covered by unit tests; this
// daemon is just the ticker/collection scaffolding, mirroring cmd/repd. It publishes no events.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/zezekim/mxsentinel/internal/config"
	"github.com/zezekim/mxsentinel/internal/healthscore"
	"github.com/zezekim/mxsentinel/internal/obs"
	chstore "github.com/zezekim/mxsentinel/internal/store/clickhouse"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
)

func main() {
	interval := flag.Duration("interval", time.Hour, "how often to recompute & snapshot every domain's health score")
	window := flag.Duration("window", healthscore.DefaultWindow, "look-back window for telemetry-derived signals")
	flag.Parse()

	if err := run(*interval, *window); err != nil {
		fmt.Fprintln(os.Stderr, "scored:", err)
		os.Exit(1)
	}
}

func run(interval, window time.Duration) error {
	cfg, err := config.Load(os.Getenv("MXS_CONFIG"))
	if err != nil {
		return err
	}
	log := obs.NewLogger("scored", cfg.LogLevel)

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

	// ClickHouse feeds the DMARC-alignment and bounce-ratio components. If it is unavailable we
	// still run — those components simply degrade to absent/neutral (documented).
	var ch *chstore.Store
	if c, cerr := chstore.New(ctx, cfg.ClickHouse); cerr != nil {
		log.Warn("clickhouse unavailable; DMARC & bounce components will degrade to neutral", "err", cerr)
	} else {
		ch = c
		defer ch.Close()
	}

	w := &worker{
		log:       log,
		pg:        pg,
		collector: healthscore.NewCollector(pg, ch).WithWindow(window),
	}
	srv.SetReady(true)
	log.Info("scored started", "interval", interval.String(), "window", window.String())

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	w.snapshot(ctx)
	for {
		select {
		case <-ctx.Done():
			log.Info("scored shutting down")
			return nil
		case <-ticker.C:
			w.snapshot(ctx)
		}
	}
}

type worker struct {
	log       *slog.Logger
	pg        *pgstore.Store
	collector *healthscore.Collector
}

// snapshot recomputes and persists a score for every monitored domain, grouping per tenant so
// the tenant/relay-level shared signals (blocklist, bounce, reputation index) are collected once.
func (w *worker) snapshot(ctx context.Context) {
	now := time.Now()
	domains, err := w.pg.AllMonitoredDomains(ctx)
	if err != nil {
		w.log.Error("list monitored domains", "err", err)
		return
	}

	byTenant := map[string][]pgstore.TenantDomainRef{}
	for _, d := range domains {
		byTenant[d.TenantID] = append(byTenant[d.TenantID], d)
	}

	var written int
	for tenantID, ds := range byTenant {
		if ctx.Err() != nil {
			return
		}
		shared, err := w.collector.CollectShared(ctx, tenantID, now)
		if err != nil {
			w.log.Error("collect shared signals", "tenant_id", tenantID, "err", err)
			continue
		}
		for _, d := range ds {
			if ctx.Err() != nil {
				return
			}
			in, err := w.collector.CollectDomain(ctx, tenantID, d.DomainName, shared, now)
			if err != nil {
				w.log.Error("collect domain signals", "tenant_id", tenantID, "domain", d.DomainName, "err", err)
				continue
			}
			res := healthscore.Compute(in, healthscore.DefaultWeights())
			comps, mErr := healthscore.MarshalComponents(res)
			if mErr != nil {
				w.log.Error("marshal components", "tenant_id", tenantID, "domain", d.DomainName, "err", mErr)
				continue
			}
			if _, err := w.pg.InsertHealthScoreSnapshot(ctx, pgstore.HealthScoreSnapshot{
				TenantID:   tenantID,
				DomainID:   d.DomainID,
				DomainName: d.DomainName,
				Score:      res.Score,
				Grade:      res.Grade,
				HasData:    res.HasData,
				Coverage:   res.Coverage,
				Components: comps,
				ComputedAt: now,
			}); err != nil {
				w.log.Error("insert snapshot", "tenant_id", tenantID, "domain", d.DomainName, "err", err)
				continue
			}
			written++
			w.log.Info("health score snapshot",
				"tenant_id", tenantID, "domain", d.DomainName,
				"score", res.Score, "grade", res.Grade, "coverage", res.Coverage)
		}
	}
	w.log.Info("scored pass complete", "domains", len(domains), "written", written)
}
