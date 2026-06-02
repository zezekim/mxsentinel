// Command incidentd persists intelligence-layer signals as queryable incidents. It
// consumes reputation.* and dns.validation_failed events and writes one incident per
// source event (idempotent). The API surfaces them at /v1/incidents. See Phase 2.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/zezekim/mxsentinel/internal/config"
	"github.com/zezekim/mxsentinel/internal/events"
	"github.com/zezekim/mxsentinel/internal/incidents"
	"github.com/zezekim/mxsentinel/internal/obs"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
	"github.com/zezekim/mxsentinel/pkg/contracts"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "incidentd:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Getenv("MXS_CONFIG"))
	if err != nil {
		return err
	}
	log := obs.NewLogger("incidentd", cfg.LogLevel)

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
	bus, err := events.Connect(cfg.NATS.URL, "incidentd", validator, log)
	if err != nil {
		return err
	}
	defer bus.Close()
	if err := bus.EnsureStreams(ctx); err != nil {
		return err
	}

	w := &worker{log: log, pg: pg}

	repCC, err := bus.Consume(ctx, events.ConsumeOptions{
		Stream: "REPUTATION", Durable: "incidentd-rep", FilterSubject: "mxs.reputation.>",
	}, w.onEvent)
	if err != nil {
		return err
	}
	defer repCC.Stop()

	dnsCC, err := bus.Consume(ctx, events.ConsumeOptions{
		Stream: "DNS", Durable: "incidentd-dns", FilterSubject: "mxs.dns.*.validation_failed",
	}, w.onEvent)
	if err != nil {
		return err
	}
	defer dnsCC.Stop()

	srv.SetReady(true)
	log.Info("incidentd started")

	<-ctx.Done()
	log.Info("incidentd shutting down")
	return nil
}

type worker struct {
	log *slog.Logger
	pg  *pgstore.Store
}

func (w *worker) onEvent(ctx context.Context, ev *contracts.Envelope) error {
	in, ok := incidents.FromEnvelope(ev)
	if !ok {
		return nil // not an incident-bearing event
	}
	id, created, err := w.pg.InsertIncident(ctx, in)
	if err != nil {
		return err // redeliver on DB error
	}
	if created {
		w.log.Info("incident recorded",
			"id", id, "kind", in.Kind, "severity", in.Severity,
			"domain", in.Domain, "title", in.Title, "source_event", in.SourceEventID)
	}
	return nil
}
