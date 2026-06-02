// Command correld is the Phase 2 correlation engine. It consumes SMTP telemetry, detects
// per-(tenant,domain,provider) rejection spikes over sliding windows, correlates each
// spike against recent DNS changes to produce a root-cause hypothesis, and publishes a
// reputation.rate_anomaly event. See internal/correlate and ARCHITECTURE.md §4.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/zezekim/mxsentinel/internal/config"
	"github.com/zezekim/mxsentinel/internal/correlate"
	"github.com/zezekim/mxsentinel/internal/events"
	"github.com/zezekim/mxsentinel/internal/obs"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
	"github.com/zezekim/mxsentinel/pkg/contracts"
)

const (
	recentMin    = 5
	baselineMin  = 55
	lookback     = 30 * time.Minute
	cooldown     = 15 * time.Minute
	tickInterval = time.Minute
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "correld:", err)
		os.Exit(1)
	}
}

func run() error {
	flag.Parse()
	cfg, err := config.Load(os.Getenv("MXS_CONFIG"))
	if err != nil {
		return err
	}
	log := obs.NewLogger("correld", cfg.LogLevel)

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
	bus, err := events.Connect(cfg.NATS.URL, "correld", validator, log)
	if err != nil {
		return err
	}
	defer bus.Close()
	if err := bus.EnsureStreams(ctx); err != nil {
		return err
	}

	w := &worker{
		log: log, pg: pg, bus: bus,
		agg:       correlate.NewAggregator(),
		cfg:       correlate.DefaultSpikeConfig(),
		lastAlert: map[string]time.Time{},
	}

	cc, err := bus.Consume(ctx, events.ConsumeOptions{
		Stream: "SMTP", Durable: "correld", FilterSubject: "mxs.smtp.>",
	}, w.onSMTP)
	if err != nil {
		return err
	}
	defer cc.Stop()

	srv.SetReady(true)
	log.Info("correld started", "recent_min", recentMin, "baseline_min", baselineMin)

	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Info("correld shutting down")
			return nil
		case <-ticker.C:
			w.evaluate(ctx)
		}
	}
}

type worker struct {
	log *slog.Logger
	pg  *pgstore.Store
	bus *events.Bus
	agg *correlate.Aggregator
	cfg correlate.SpikeConfig

	mu        sync.Mutex
	lastAlert map[string]time.Time
}

// onSMTP folds an SMTP event into the rolling aggregator.
func (w *worker) onSMTP(_ context.Context, ev *contracts.Envelope) error {
	var p contracts.SMTPPayload
	if err := ev.DecodePayload(&p); err != nil {
		return nil // malformed payload: drop, don't redeliver forever
	}
	if p.FromDomain == "" {
		return nil
	}
	rejected := p.Outcome != "delivered" && p.Outcome != "received"
	var reason correlate.ReasonCategory
	if rejected {
		reason = correlate.ClassifyRejection(p.SMTPCode, p.EnhancedStatus, p.ResponseText, p.Provider).Category
	}
	w.agg.Observe(
		correlate.Key{TenantID: ev.TenantID, Domain: p.FromDomain, Provider: p.Provider},
		parseTime(ev.OccurredAt), rejected, reason,
	)
	return nil
}

// evaluate detects spikes, correlates them, and emits anomalies (respecting a cooldown).
func (w *worker) evaluate(ctx context.Context) {
	now := time.Now()
	for _, sp := range w.agg.Evaluate(now, recentMin, baselineMin, w.cfg) {
		key := sp.TenantID + "|" + sp.Domain + "|" + sp.Provider
		w.mu.Lock()
		last, seen := w.lastAlert[key]
		w.mu.Unlock()
		if seen && now.Sub(last) < cooldown {
			continue
		}

		var changes []correlate.DNSChange
		if rows, err := w.pg.RecentDNSChanges(ctx, sp.TenantID, sp.Domain, sp.WindowStart.Add(-lookback)); err != nil {
			w.log.Warn("recent dns changes lookup failed", "domain", sp.Domain, "err", err)
		} else {
			changes = toDNSChanges(rows)
		}

		corr := correlate.Correlate(sp, changes, lookback)
		w.publish(ctx, sp, corr)
		w.log.Info("rejection spike correlated",
			"domain", sp.Domain, "provider", sp.Provider, "reason", sp.DominantReason,
			"rejections", sp.Rejections, "confidence", corr.Confidence, "root_cause", corr.RootCause)

		w.mu.Lock()
		w.lastAlert[key] = now
		w.mu.Unlock()
	}
	w.agg.Prune(now, recentMin+baselineMin+10)
}

func (w *worker) publish(ctx context.Context, sp correlate.Spike, corr correlate.Correlation) {
	severity := "warning"
	if corr.Confidence >= 0.8 {
		severity = "critical"
	}
	detail := map[string]any{
		"reason":      string(sp.DominantReason),
		"root_cause":  corr.RootCause,
		"confidence":  corr.Confidence,
		"remediation": corr.Remediation,
		"provider":    sp.Provider,
		"rejections":  sp.Rejections,
	}
	if corr.RelatedChange != nil {
		detail["related_snapshot"] = corr.RelatedChange.SnapshotID
		detail["dns_changed_at"] = corr.RelatedChange.At.UTC().Format(time.RFC3339)
	}

	ev, err := events.NewEnvelope(contracts.EventReputationRateAnomaly, sp.TenantID, "correld",
		contracts.Correlation{Domain: sp.Domain}, time.Now(),
		contracts.ReputationPayload{
			Signal: "rate_anomaly", SubjectKind: "domain", Subject: sp.Domain,
			Severity: severity, Source: sp.Provider,
			ObservedValue: float64(sp.Rejections), WindowSecs: recentMin * 60,
			Detail: detail,
		})
	if err != nil {
		w.log.Error("build rate_anomaly event", "err", err)
		return
	}
	if err := w.bus.Publish(ctx, ev); err != nil {
		w.log.Error("publish rate_anomaly", "domain", sp.Domain, "err", err)
	}
}

func toDNSChanges(rows []pgstore.DNSChangeRow) []correlate.DNSChange {
	out := make([]correlate.DNSChange, 0, len(rows))
	for _, r := range rows {
		out = append(out, correlate.DNSChange{SnapshotID: r.SnapshotID, At: r.CapturedAt, Findings: r.Findings})
	}
	return out
}

func parseTime(s string) time.Time {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	return time.Now().UTC()
}
