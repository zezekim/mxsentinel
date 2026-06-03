// Command aid is the Phase 3 AI diagnostics daemon. Two jobs, both metadata-only
// (ARCHITECTURE.md §5):
//
//	RCA      — poll incidents that need analysis, enrich them with recent historical
//	           context (deliverability + prior incidents), ask a local LLM for a
//	           root-cause narrative + remediation, write it back, and publish ai.rca.
//	Anomaly  — periodically classify each tenant/provider's recent deliverability window
//	           and publish ai.anomaly for non-normal classifications.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/zezekim/mxsentinel/internal/ai"
	"github.com/zezekim/mxsentinel/internal/config"
	"github.com/zezekim/mxsentinel/internal/events"
	"github.com/zezekim/mxsentinel/internal/obs"
	chstore "github.com/zezekim/mxsentinel/internal/store/clickhouse"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
	"github.com/zezekim/mxsentinel/pkg/contracts"
)

const (
	rcaContextWindow = 6 * time.Hour
	anomalyWindow    = 15 * time.Minute
	anomalyMinVolume = 20
	anomalyCooldown  = 30 * time.Minute
)

func main() {
	interval := flag.Duration("interval", 30*time.Second, "how often to analyze pending incidents")
	anomalyEvery := flag.Duration("anomaly-interval", 5*time.Minute, "how often to run anomaly classification")
	batch := flag.Int("batch", 10, "max incidents to analyze per cycle")
	flag.Parse()

	if err := run(*interval, *anomalyEvery, *batch); err != nil {
		fmt.Fprintln(os.Stderr, "aid:", err)
		os.Exit(1)
	}
}

func run(interval, anomalyEvery time.Duration, batch int) error {
	cfg, err := config.Load(os.Getenv("MXS_CONFIG"))
	if err != nil {
		return err
	}
	log := obs.NewLogger("aid", cfg.LogLevel)

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

	ch, err := chstore.New(ctx, cfg.ClickHouse)
	if err != nil {
		return err
	}
	defer ch.Close()

	validator, err := events.NewValidator()
	if err != nil {
		return err
	}
	bus, err := events.Connect(cfg.NATS.URL, "aid", validator, log)
	if err != nil {
		return err
	}
	defer bus.Close()
	if err := bus.EnsureStreams(ctx); err != nil {
		return err
	}

	w := &worker{
		log: log, pg: pg, ch: ch, bus: bus,
		client:      ai.NewOpenAIClient(cfg.AI.Endpoint, cfg.AI.APIKey, cfg.AI.Model, time.Duration(cfg.AI.TimeoutSecs)*time.Second),
		model:       cfg.AI.Model,
		batch:       batch,
		lastAnomaly: map[string]time.Time{},
	}
	srv.SetReady(true)
	log.Info("aid started", "endpoint", cfg.AI.Endpoint, "model", cfg.AI.Model,
		"rca_interval", interval.String(), "anomaly_interval", anomalyEvery.String())

	rcaTick := time.NewTicker(interval)
	defer rcaTick.Stop()
	anomalyTick := time.NewTicker(anomalyEvery)
	defer anomalyTick.Stop()

	w.runRCA(ctx)
	for {
		select {
		case <-ctx.Done():
			log.Info("aid shutting down")
			return nil
		case <-rcaTick.C:
			w.runRCA(ctx)
		case <-anomalyTick.C:
			w.runAnomaly(ctx)
		}
	}
}

type worker struct {
	log    *slog.Logger
	pg     *pgstore.Store
	ch     *chstore.Store
	bus    *events.Bus
	client ai.Client
	model  string
	batch  int

	mu          sync.Mutex
	lastAnomaly map[string]time.Time
}

// ---- RCA -------------------------------------------------------------------

func (w *worker) runRCA(ctx context.Context) {
	incidents, err := w.pg.ListIncidentsNeedingAI(ctx, w.batch)
	if err != nil {
		w.log.Error("list incidents needing ai", "err", err)
		return
	}
	for _, inc := range incidents {
		if ctx.Err() != nil {
			return
		}
		in := toAIIncident(inc)
		in.Context = w.buildContext(ctx, inc)

		payload, err := ai.Diagnose(ctx, w.client, in, w.model)
		if err != nil {
			// LLM likely unavailable for the whole batch — defer; retried next tick.
			w.log.Warn("diagnose failed; deferring remaining incidents", "incident", inc.ID, "err", err)
			return
		}
		w.persistAndPublish(ctx, inc, payload)
	}
}

// buildContext gathers recent historical signal for the incident's tenant/domain.
func (w *worker) buildContext(ctx context.Context, inc pgstore.Incident) map[string]any {
	c := map[string]any{}
	now := time.Now()

	if inc.Domain != "" {
		if titles, err := w.pg.RecentIncidentTitles(ctx, inc.TenantID, inc.Domain, inc.ID, 5); err == nil && len(titles) > 0 {
			c["prior_incidents"] = titles
		}
	}
	if stats, err := w.ch.DeliverabilityByProvider(ctx, inc.TenantID, now.Add(-rcaContextWindow), time.Time{}); err == nil && len(stats) > 0 {
		c["recent_deliverability_6h"] = summarizeStats(stats)
	}
	if len(c) == 0 {
		return nil
	}
	return c
}

func (w *worker) persistAndPublish(ctx context.Context, inc pgstore.Incident, payload contracts.AIPayload) {
	rem, _ := json.Marshal(payload.Recommendations)
	if err := w.pg.SetIncidentAI(ctx, inc.ID, payload.Narrative, rem, payload.Model); err != nil {
		w.log.Error("persist ai analysis", "incident", inc.ID, "err", err)
		return
	}
	ev, err := events.NewEnvelope(contracts.EventAIRCA, inc.TenantID, "aid",
		contracts.Correlation{Domain: inc.Domain}, time.Now(), payload)
	if err != nil {
		w.log.Error("build ai.rca", "err", err)
		return
	}
	if err := w.bus.Publish(ctx, ev); err != nil {
		w.log.Error("publish ai.rca", "incident", inc.ID, "err", err)
	}
	w.log.Info("incident analyzed", "incident", inc.ID, "kind", inc.Kind,
		"confidence", payload.Confidence, "recommendations", len(payload.Recommendations))
}

// ---- Anomaly classification ------------------------------------------------

func (w *worker) runAnomaly(ctx context.Context) {
	domains, err := w.pg.ListMonitoredDomains(ctx)
	if err != nil {
		w.log.Error("list domains for anomaly pass", "err", err)
		return
	}
	seen := map[string]bool{}
	now := time.Now()
	for _, d := range domains {
		if ctx.Err() != nil {
			return
		}
		if seen[d.TenantID] {
			continue
		}
		seen[d.TenantID] = true

		stats, err := w.ch.DeliverabilityByProvider(ctx, d.TenantID, now.Add(-anomalyWindow), time.Time{})
		if err != nil {
			w.log.Warn("deliverability for anomaly pass failed", "tenant", d.TenantID, "err", err)
			continue
		}
		for _, s := range stats {
			if s.Total < anomalyMinVolume {
				continue
			}
			if !w.classifyAndAlert(ctx, d.TenantID, s, now) {
				return // LLM unavailable — stop this cycle
			}
		}
	}
}

// classifyAndAlert returns false if the LLM call failed (caller should stop the cycle).
func (w *worker) classifyAndAlert(ctx context.Context, tenantID string, s chstore.ProviderStats, now time.Time) bool {
	key := tenantID + "|" + s.Provider
	w.mu.Lock()
	last, seen := w.lastAnomaly[key]
	w.mu.Unlock()
	if seen && now.Sub(last) < anomalyCooldown {
		return true
	}

	in := ai.AnomalyInput{
		Provider: s.Provider,
		Window:   "last 15m",
		Stats: map[string]any{
			"delivered": s.Delivered, "deferred": s.Deferred,
			"bounced": s.Bounced, "rejected": s.Rejected, "total": s.Total,
		},
	}
	payload, anom, err := ai.ClassifyAnomaly(ctx, w.client, in, w.model)
	if err != nil {
		w.log.Warn("anomaly classify failed; deferring", "tenant", tenantID, "provider", s.Provider, "err", err)
		return false
	}
	if anom.Classification == "none" || (anom.Severity != "warning" && anom.Severity != "critical") {
		return true
	}

	ev, err := events.NewEnvelope(contracts.EventAIAnomaly, tenantID, "aid",
		contracts.Correlation{}, now, payload)
	if err != nil {
		w.log.Error("build ai.anomaly", "err", err)
		return true
	}
	if err := w.bus.Publish(ctx, ev); err != nil {
		w.log.Error("publish ai.anomaly", "tenant", tenantID, "err", err)
		return true
	}
	w.mu.Lock()
	w.lastAnomaly[key] = now
	w.mu.Unlock()
	w.log.Info("anomaly classified", "tenant", tenantID, "provider", s.Provider,
		"classification", anom.Classification, "severity", anom.Severity, "confidence", anom.Confidence)
	return true
}

// ---- helpers ---------------------------------------------------------------

func toAIIncident(inc pgstore.Incident) ai.Incident {
	return ai.Incident{
		SourceEventID: inc.SourceEventID,
		Kind:          inc.Kind,
		Severity:      inc.Severity,
		Domain:        inc.Domain,
		Subject:       inc.Subject,
		Title:         inc.Title,
		Detail:        inc.Detail,
		Confidence:    inc.Confidence,
	}
}

func summarizeStats(stats []chstore.ProviderStats) []map[string]any {
	out := make([]map[string]any, 0, len(stats))
	for _, s := range stats {
		out = append(out, map[string]any{
			"provider": s.Provider, "total": s.Total,
			"delivered": s.Delivered, "deferred": s.Deferred,
			"bounced": s.Bounced, "rejected": s.Rejected,
		})
	}
	return out
}
