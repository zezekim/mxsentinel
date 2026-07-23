// Command relayfailoverd is a circuit breaker that reroutes a receiver provider's outbound
// mail to a fallback smarthost (e.g. mail.baby) when the relay's DIRECT delivery to that
// provider is sustainedly deferred with TRANSIENT 4xx codes — the "keep bouncing off
// Outlook" case, but only when it's throttling (retryable), not a spam/reputation block.
//
// Why only transient 4xx: rerouting spam/reputation-BLOCKED mail (5xx) through a fallback
// relay just launders the same reputation problem onto the fallback's IPs and violates most
// relay providers' terms. Those are fixed at the source, not by failover. The ClickHouse
// query (internal/store/clickhouse.ProviderDeferStats) isolates 4xx defers from
// spam/reputation blocks for exactly this reason.
//
// Mechanism (mirrors rbld's healthy-IPs design): on each tick the daemon measures the
// relay-wide transient-defer rate to the provider over a sliding window and runs a circuit
// breaker (internal/relayfailover). It writes the set of recipient domains currently in
// failover to a STATE FILE; a host-side hook (deploy/hooks/relay-failover-hook.sh, run by
// cron) reads it, rebuilds the Postfix transport overlay routing those domains to the
// fallback transport, and requeues deferred mail (`postsuper -r`). relayfailoverd never
// touches host Postfix — so the mail path is unaffected if this daemon is down (the overlay
// simply stops changing). Recovery is time-based: after HoldFor the breaker reverts to
// DIRECT to re-probe the provider with real traffic; if it's still throttling it re-trips.
//
// Configuration (env): MXS_FAILOVER_ENABLED, MXS_FAILOVER_PROVIDER, MXS_FAILOVER_DOMAINS,
// MXS_FAILOVER_STATE_FILE, MXS_FAILOVER_INTERVAL, MXS_FAILOVER_WINDOW, MXS_FAILOVER_HOLD,
// MXS_FAILOVER_TRIP_RATE, MXS_FAILOVER_MIN_ATTEMPTS, MXS_FAILOVER_MIN_DEFERS,
// MXS_FAILOVER_MAX_DOMAINS, RELAY_TENANT_ID. See docs/relay-failover.md.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/zezekim/mxsentinel/internal/config"
	"github.com/zezekim/mxsentinel/internal/obs"
	"github.com/zezekim/mxsentinel/internal/relayfailover"
	chstore "github.com/zezekim/mxsentinel/internal/store/clickhouse"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
)

// failoverNamespace is a fixed UUIDv5 namespace so a given (provider, opened-at) episode
// maps to a stable source_event_id — InsertIncident's UNIQUE(tenant, source) dedupes
// re-evaluations within one open episode.
var failoverNamespace = uuid.MustParse("b2c9f7a1-3d4e-5f60-8a1b-2c3d4e5f6a7b")

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "relayfailoverd:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Getenv("MXS_CONFIG"))
	if err != nil {
		return err
	}
	fcfg := relayfailover.LoadConfig()
	log := obs.NewLogger("relayfailoverd", cfg.LogLevel)

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

	if !fcfg.Enabled {
		log.Warn("relayfailoverd is DISABLED (set MXS_FAILOVER_ENABLED=true to arm). " +
			"Idling — will not evaluate or write the failover state file.")
		srv.SetReady(true)
		<-ctx.Done()
		return nil
	}
	if len(fcfg.RecipientDomains) == 0 {
		return fmt.Errorf("MXS_FAILOVER_ENABLED=true but no MXS_FAILOVER_DOMAINS resolved")
	}
	if fcfg.Policy.MaxDomains > 0 && len(fcfg.RecipientDomains) > fcfg.Policy.MaxDomains {
		return fmt.Errorf("MXS_FAILOVER_DOMAINS has %d entries, exceeds MXS_FAILOVER_MAX_DOMAINS=%d",
			len(fcfg.RecipientDomains), fcfg.Policy.MaxDomains)
	}

	pg, err := pgstore.New(ctx, cfg.Postgres)
	if err != nil {
		return err
	}
	defer pg.Close()

	var ch *chstore.Store
	if err := obs.Retry(ctx, log, "clickhouse", 20, 3*time.Second, func() (e error) {
		ch, e = chstore.New(ctx, cfg.ClickHouse)
		return
	}); err != nil {
		return err
	}
	defer ch.Close()

	// Resolve the incident tenant (optional). Empty -> incidents are skipped (status still
	// works via logs + the state file), matching probed's behaviour.
	var tenantID string
	if fcfg.TenantID != "" {
		tenantID = fcfg.TenantID
	}

	w := &worker{log: log, pg: pg, ch: ch, cfg: fcfg, breaker: relayfailover.NewBreaker(), tenantID: tenantID}

	// Write the initial (closed/empty) state so a stale file from a previous run can't leave
	// domains stuck in failover after a restart.
	if err := relayfailover.WriteDomains(fcfg.StateFile, nil); err != nil {
		log.Error("write initial failover state file", "file", fcfg.StateFile, "err", err)
	}

	srv.SetReady(true)
	log.Info("relayfailoverd started",
		"provider", fcfg.Provider, "domains", len(fcfg.RecipientDomains),
		"window", fcfg.Window.String(), "interval", fcfg.Interval.String(),
		"trip_rate", fcfg.Policy.TripRate, "hold", fcfg.Policy.HoldFor.String(),
		"state_file", fcfg.StateFile, "incidents", tenantID != "")

	w.tick(ctx) // evaluate immediately on start
	ticker := time.NewTicker(fcfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Info("relayfailoverd shutting down")
			return nil
		case <-ticker.C:
			w.tick(ctx)
		}
	}
}

type worker struct {
	log      *slog.Logger
	pg       *pgstore.Store
	ch       *chstore.Store
	cfg      relayfailover.Config
	breaker  *relayfailover.Breaker
	tenantID string
}

func (w *worker) tick(ctx context.Context) {
	now := time.Now().UTC()
	stats, err := w.ch.ProviderDeferStats(ctx, w.cfg.Provider, now.Add(-w.cfg.Window))
	if err != nil {
		// A query error must never move the breaker — treat as "no new information" and keep
		// the current state. (Availability: a ClickHouse blip should not silently reroute all
		// mail, nor yank it back.)
		w.log.Error("query provider defer stats; holding current failover state",
			"provider", w.cfg.Provider, "state", w.breaker.State, "err", err)
		return
	}

	sample := relayfailover.Sample{Attempts: stats.Attempts, Deferred4xx: stats.Deferred4xx}
	tr := w.breaker.Evaluate(now, sample, w.cfg.Policy)

	// Desired domain set: full list when OPEN, empty when CLOSED.
	var domains []string
	if w.breaker.State == relayfailover.StateOpen {
		domains = w.cfg.RecipientDomains
	}
	if err := relayfailover.WriteDomains(w.cfg.StateFile, domains); err != nil {
		w.log.Error("write failover state file", "file", w.cfg.StateFile, "err", err)
	}

	if tr.Changed {
		w.log.Warn("failover breaker transition",
			"provider", w.cfg.Provider, "from", tr.From, "to", tr.To, "reason", tr.Reason,
			"attempts", stats.Attempts, "deferred_4xx", stats.Deferred4xx,
			"defer_rate", stats.DeferRate(), "spam_rep_context", stats.SpamRepBlock,
			"domains_in_failover", len(domains))
		w.recordIncident(ctx, tr, stats, now)
	} else {
		w.log.Debug("failover tick",
			"provider", w.cfg.Provider, "state", w.breaker.State, "reason", tr.Reason,
			"attempts", stats.Attempts, "deferred_4xx", stats.Deferred4xx, "defer_rate", stats.DeferRate())
	}
}

func (w *worker) recordIncident(ctx context.Context, tr relayfailover.Transition, stats chstore.ProviderDeferStats, _ time.Time) {
	if w.tenantID == "" || tr.To != relayfailover.StateOpen {
		return // incidents are opened on trip only; revert is logged, not incidented
	}
	detail, _ := json.Marshal(map[string]any{
		"provider":            w.cfg.Provider,
		"attempts":            stats.Attempts,
		"deferred_4xx":        stats.Deferred4xx,
		"defer_rate":          stats.DeferRate(),
		"spam_rep_blocks":     stats.SpamRepBlock,
		"window":              w.cfg.Window.String(),
		"domains_failed_over": w.cfg.RecipientDomains,
		"hold_for":            w.cfg.Policy.HoldFor.String(),
		"reason":              tr.Reason,
		"remediation": "Outbound mail to " + w.cfg.Provider + " is being transiently deferred (4xx) and has " +
			"been rerouted to the fallback smarthost. This is a stopgap: transient defers usually mean " +
			"per-IP throttling/warmup or a temporary block. Investigate the sending IP's reputation, volume " +
			"ramp, and auth alignment. The breaker auto-reverts to direct delivery after the hold window to re-probe.",
	})
	source := uuid.NewSHA1(failoverNamespace, []byte(w.cfg.Provider+"|"+w.breaker.OpenedAt.Format(time.RFC3339))).String()
	if _, _, err := w.pg.InsertIncident(ctx, pgstore.IncidentInput{
		TenantID:      w.tenantID,
		SourceEventID: source,
		Kind:          "other",
		Severity:      "high",
		Domain:        w.cfg.Provider,
		Subject:       w.cfg.Provider,
		Title:         "Outbound mail to " + w.cfg.Provider + " rerouted to fallback relay (transient 4xx defers)",
		Detail:        detail,
	}); err != nil {
		w.log.Error("open failover incident", "provider", w.cfg.Provider, "err", err)
	}
}
