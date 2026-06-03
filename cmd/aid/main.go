// Command aid is the Phase 3 AI diagnostics daemon. It polls incidents that have not yet
// been analyzed, asks a local LLM (Ollama/vLLM, via internal/ai) for a metadata-only
// root-cause narrative + remediation, writes that back onto the incident, and publishes
// an ai.rca event. See ARCHITECTURE.md §5.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/zezekim/mxsentinel/internal/ai"
	"github.com/zezekim/mxsentinel/internal/config"
	"github.com/zezekim/mxsentinel/internal/events"
	"github.com/zezekim/mxsentinel/internal/obs"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
	"github.com/zezekim/mxsentinel/pkg/contracts"
)

func main() {
	interval := flag.Duration("interval", 30*time.Second, "how often to look for incidents needing analysis")
	batch := flag.Int("batch", 10, "max incidents to analyze per cycle")
	flag.Parse()

	if err := run(*interval, *batch); err != nil {
		fmt.Fprintln(os.Stderr, "aid:", err)
		os.Exit(1)
	}
}

func run(interval time.Duration, batch int) error {
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
		log:    log,
		pg:     pg,
		bus:    bus,
		client: ai.NewOpenAIClient(cfg.AI.Endpoint, cfg.AI.APIKey, cfg.AI.Model, time.Duration(cfg.AI.TimeoutSecs)*time.Second),
		model:  cfg.AI.Model,
		batch:  batch,
	}
	srv.SetReady(true)
	log.Info("aid started", "endpoint", cfg.AI.Endpoint, "model", cfg.AI.Model, "interval", interval.String())

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	w.runOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			log.Info("aid shutting down")
			return nil
		case <-ticker.C:
			w.runOnce(ctx)
		}
	}
}

type worker struct {
	log    *slog.Logger
	pg     *pgstore.Store
	bus    *events.Bus
	client ai.Client
	model  string
	batch  int
}

func (w *worker) runOnce(ctx context.Context) {
	incidents, err := w.pg.ListIncidentsNeedingAI(ctx, w.batch)
	if err != nil {
		w.log.Error("list incidents needing ai", "err", err)
		return
	}
	for _, inc := range incidents {
		if ctx.Err() != nil {
			return
		}
		payload, err := ai.Diagnose(ctx, w.client, toAIIncident(inc), w.model)
		if err != nil {
			// The LLM is likely unavailable for the whole batch — stop this cycle and
			// leave these incidents unanalyzed so they're retried next tick.
			w.log.Warn("diagnose failed; deferring remaining incidents", "incident", inc.ID, "err", err)
			return
		}
		w.persistAndPublish(ctx, inc, payload)
	}
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
