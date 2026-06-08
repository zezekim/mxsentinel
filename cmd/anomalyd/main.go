// Command anomalyd is the send-volume anomaly-detection daemon. It consumes SMTP telemetry
// from the bus (mxs.smtp.>), keeps a per-(tenant, sender_domain) rolling hourly message
// count, and maintains an EWMA hourly baseline persisted in Postgres
// (sender_volume_baseline). When a domain's current-hour count exceeds
// max(baseline*ANOMALY_FACTOR, ANOMALY_MIN_ABS) — once it's warmed up
// (samples >= ANOMALY_MIN_SAMPLES) — it logs a row to volume_anomaly and opens a critical
// incident, then applies a per-domain cooldown.
//
// It keys on the SENDING DOMAIN, not the SASL login: a shared hosting relay authenticates
// with one credential for hundreds of client domains, so a spike must be isolated per
// client. This complements rspamd's inline rate caps (deploy/rspamd/ratelimit.conf):
// rspamd holds an absolute ceiling, anomalyd catches relative spikes below that ceiling.
//
// SHARED-RELAY DEGENERACY: domains need ~ANOMALY_MIN_SAMPLES completed hours (~6h) of
// warm-up before any trip can fire, and only outbound events carrying a from_domain are
// attributable. See docs/deploy-relay.md.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/zezekim/mxsentinel/internal/anomaly"
	"github.com/zezekim/mxsentinel/internal/config"
	"github.com/zezekim/mxsentinel/internal/events"
	"github.com/zezekim/mxsentinel/internal/obs"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
	"github.com/zezekim/mxsentinel/pkg/contracts"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "anomalyd:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Getenv("MXS_CONFIG"))
	if err != nil {
		return err
	}
	log := obs.NewLogger("anomalyd", cfg.LogLevel)

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
	bus, err := events.Connect(cfg.NATS.URL, "anomalyd", validator, log)
	if err != nil {
		return err
	}
	defer bus.Close()
	if err := bus.EnsureStreams(ctx); err != nil {
		return err
	}

	acfg := anomaly.LoadConfig()
	store := anomaly.NewStore(pg.Pool)
	det := anomaly.NewDetector(acfg, store, incidentAdapter{pg}, log)

	w := &worker{log: log.With("component", "consumer"), det: det}

	cc, err := bus.Consume(ctx, events.ConsumeOptions{
		Stream: "SMTP", Durable: "anomalyd", FilterSubject: "mxs.smtp.>",
	}, w.onSMTP)
	if err != nil {
		return err
	}
	defer cc.Stop()

	srv.SetReady(true)
	log.Info("anomalyd started",
		"factor", acfg.Factor, "min_abs", acfg.MinAbsolute, "min_samples", acfg.MinSamples,
		"ewma_alpha", acfg.EWMAAlpha, "cooldown", acfg.Cooldown.String())

	// Sweep on the minute to roll over completed hours for quiet domains and prune idle
	// counters even when no events arrive.
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Info("anomalyd shutting down")
			return nil
		case <-ticker.C:
			det.Sweep(ctx, time.Now())
		}
	}
}

// worker adapts bus events to detector Records.
type worker struct {
	log *slog.Logger
	det *anomaly.Detector
}

// onSMTP folds one SMTP event into its sending domain's hourly counter. Only outbound
// mail attributable to a From: domain is counted; everything else is acked and ignored.
func (w *worker) onSMTP(ctx context.Context, ev *contracts.Envelope) error {
	var p contracts.SMTPPayload
	if err := ev.DecodePayload(&p); err != nil {
		return nil // malformed: drop, don't redeliver forever
	}
	if p.FromDomain == "" {
		return nil // not attributable to a sending domain
	}
	w.det.Record(ctx, ev.TenantID, p.FromDomain, time.Now())
	return nil
}

// incidentAdapter bridges *postgres.Store to the anomaly.Incidenter interface, translating
// the package-local IncidentInput to the store's IncidentInput (identical field set).
type incidentAdapter struct{ pg *pgstore.Store }

func (a incidentAdapter) InsertIncident(ctx context.Context, in anomaly.IncidentInput) (string, bool, error) {
	return a.pg.InsertIncident(ctx, pgstore.IncidentInput{
		TenantID:      in.TenantID,
		SourceEventID: in.SourceEventID,
		Kind:          in.Kind,
		Severity:      in.Severity,
		Domain:        in.Domain,
		Subject:       in.Subject,
		Title:         in.Title,
		Detail:        in.Detail,
		Confidence:    in.Confidence,
	})
}
