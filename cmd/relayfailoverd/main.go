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
	"path/filepath"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/zezekim/mxsentinel/internal/config"
	"github.com/zezekim/mxsentinel/internal/crypto"
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

	// The daemon runs when either the env flag is set OR a relay tenant is configured (the
	// dashboard-managed smarthost config lives on that tenant and may enable failover without
	// any env flag). Only a deployment with neither idles.
	if !fcfg.Enabled && fcfg.TenantID == "" {
		log.Warn("relayfailoverd is DISABLED (no MXS_FAILOVER_ENABLED and no RELAY_TENANT_ID). " +
			"Idling — configure the fallback smarthost in the dashboard (Settings) or set the env flags.")
		srv.SetReady(true)
		<-ctx.Done()
		return nil
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

	// Encryptor to open the dashboard-sealed smarthost password. nil = passthrough (plaintext),
	// consistent with apid when MXS_ENCRYPTION_KEY is unset.
	enc, encrypted, err := crypto.NewEncryptor(cfg.Integration.EncryptionKey)
	if err != nil {
		return fmt.Errorf("init encryptor: %w", err)
	}
	if !encrypted {
		log.Warn("MXS_ENCRYPTION_KEY not set — smarthost password read as plaintext")
	}

	w := &worker{
		log: log, pg: pg, ch: ch, cfg: fcfg, enc: enc,
		breaker:  relayfailover.NewBreaker(),
		tenantID: fcfg.TenantID,
		stateDir: filepath.Dir(fcfg.StateFile),
	}

	// Write the initial (closed/empty) state so a stale file from a previous run can't leave
	// domains stuck in failover after a restart.
	if err := relayfailover.WriteDomains(fcfg.StateFile, nil); err != nil {
		log.Error("write initial failover state file", "file", fcfg.StateFile, "err", err)
	}

	srv.SetReady(true)
	log.Info("relayfailoverd started",
		"env_enabled", fcfg.Enabled, "tenant_configured", fcfg.TenantID != "",
		"window", fcfg.Window.String(), "interval", fcfg.Interval.String(),
		"state_file", fcfg.StateFile, "incidents", fcfg.TenantID != "")

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
	enc      *crypto.Encryptor
	breaker  *relayfailover.Breaker
	tenantID string
	stateDir string
}

// effective is the resolved failover config for one tick: the dashboard-managed smarthost
// config (on the relay tenant) when present+configured, else the env-based config.
type effective struct {
	enabled  bool
	mode     string // "always" | "on_throttle"
	provider string
	domains  []string
	window   time.Duration
	policy   relayfailover.Policy
	// credentials to render for the host hook (empty when unset/env-only)
	host     string
	port     int
	username string
	password string
}

func (w *worker) tick(ctx context.Context) {
	now := time.Now().UTC()
	eff := w.resolveEffective(ctx)

	// Render (or tear down) the smarthost credentials + transport for the host hook. When the
	// smarthost is disabled or has no creds, this removes the rendered files.
	if eff.enabled {
		if err := relayfailover.RenderSmarthost(w.stateDir, eff.host, eff.port, eff.username, eff.password); err != nil {
			w.log.Error("render smarthost creds", "err", err)
		}
	} else {
		_ = relayfailover.RenderSmarthost(w.stateDir, "", 0, "", "") // clears the files
	}

	// Decide the failover domain set.
	var domains []string
	switch {
	case !eff.enabled:
		domains = nil // clears the overlay
	case eff.mode == "always":
		// Pinned route: always send these domains via the smarthost, regardless of defer rate.
		domains = eff.domains
	default: // "on_throttle" — the circuit breaker
		stats, err := w.ch.ProviderDeferStats(ctx, eff.provider, now.Add(-eff.window))
		if err != nil {
			// A query error must never move the breaker — hold the current state. (A ClickHouse
			// blip should not silently reroute all mail, nor yank it back.)
			w.log.Error("query provider defer stats; holding current failover state",
				"provider", eff.provider, "state", w.breaker.State, "err", err)
			return
		}
		sample := relayfailover.Sample{Attempts: stats.Attempts, Deferred4xx: stats.Deferred4xx}
		tr := w.breaker.Evaluate(now, sample, eff.policy)
		if w.breaker.State == relayfailover.StateOpen {
			domains = eff.domains
		}
		if tr.Changed {
			w.log.Warn("failover breaker transition",
				"provider", eff.provider, "from", tr.From, "to", tr.To, "reason", tr.Reason,
				"attempts", stats.Attempts, "deferred_4xx", stats.Deferred4xx,
				"defer_rate", stats.DeferRate(), "spam_rep_context", stats.SpamRepBlock,
				"domains_in_failover", len(domains))
			w.recordIncident(ctx, tr, stats, eff)
		}
	}

	if eff.policy.MaxDomains > 0 && len(domains) > eff.policy.MaxDomains {
		w.log.Warn("failover domain set exceeds cap; truncating",
			"count", len(domains), "cap", eff.policy.MaxDomains)
		domains = domains[:eff.policy.MaxDomains]
	}
	if err := relayfailover.WriteDomains(w.cfg.StateFile, domains); err != nil {
		w.log.Error("write failover state file", "file", w.cfg.StateFile, "err", err)
	}
}

// resolveEffective merges the dashboard smarthost config (source of truth when configured)
// over the env-based config.
func (w *worker) resolveEffective(ctx context.Context) effective {
	// Start from env config (breaker-oriented; no creds).
	eff := effective{
		enabled:  w.cfg.Enabled,
		mode:     "on_throttle",
		provider: w.cfg.Provider,
		domains:  w.cfg.RecipientDomains,
		window:   w.cfg.Window,
		policy:   w.cfg.Policy,
	}
	if w.tenantID == "" {
		return eff
	}
	sh, err := w.pg.GetSmarthostSettings(ctx, w.tenantID)
	if err != nil {
		w.log.Error("read smarthost settings; using env config", "err", err)
		return eff
	}
	if sh.Host == "" && !sh.Enabled && len(sh.Domains) == 0 {
		return eff // not configured in the dashboard — keep env behaviour
	}

	// Dashboard config is the source of truth once present.
	eff.enabled = sh.Enabled
	eff.mode = sh.Mode
	if eff.mode == "" {
		eff.mode = "always"
	}
	eff.domains = sh.Domains
	eff.host = sh.Host
	eff.port = sh.Port
	eff.username = sh.Username
	if sh.PasswordEnc != "" && w.enc != nil {
		if pw, oerr := w.enc.Open(sh.PasswordEnc); oerr == nil {
			eff.password = pw
		} else {
			w.log.Error("open smarthost password; smarthost auth will fail", "err", oerr)
		}
	} else if sh.PasswordEnc != "" {
		eff.password = sh.PasswordEnc // no encryptor: stored as plaintext
	}
	// Optional breaker tuning overrides.
	if sh.TripRate > 0 {
		eff.policy.TripRate = sh.TripRate
	}
	if sh.WindowSecs > 0 {
		eff.window = time.Duration(sh.WindowSecs) * time.Second
	}
	if sh.HoldSecs > 0 {
		eff.policy.HoldFor = time.Duration(sh.HoldSecs) * time.Second
	}
	if sh.MinAttempts > 0 {
		eff.policy.MinAttempts = uint64(sh.MinAttempts)
	}
	if sh.MinDefers > 0 {
		eff.policy.MinDefers = uint64(sh.MinDefers)
	}
	return eff
}

func (w *worker) recordIncident(ctx context.Context, tr relayfailover.Transition, stats chstore.ProviderDeferStats, eff effective) {
	if w.tenantID == "" || tr.To != relayfailover.StateOpen {
		return // incidents are opened on trip only; revert is logged, not incidented
	}
	detail, _ := json.Marshal(map[string]any{
		"provider":            eff.provider,
		"attempts":            stats.Attempts,
		"deferred_4xx":        stats.Deferred4xx,
		"defer_rate":          stats.DeferRate(),
		"spam_rep_blocks":     stats.SpamRepBlock,
		"window":              eff.window.String(),
		"domains_failed_over": eff.domains,
		"hold_for":            eff.policy.HoldFor.String(),
		"reason":              tr.Reason,
		"remediation": "Outbound mail to " + eff.provider + " is being transiently deferred (4xx) and has " +
			"been rerouted to the fallback smarthost. This is a stopgap: transient defers usually mean " +
			"per-IP throttling/warmup or a temporary block. Investigate the sending IP's reputation, volume " +
			"ramp, and auth alignment. The breaker auto-reverts to direct delivery after the hold window to re-probe.",
	})
	source := uuid.NewSHA1(failoverNamespace, []byte(eff.provider+"|"+w.breaker.OpenedAt.Format(time.RFC3339))).String()
	if _, _, err := w.pg.InsertIncident(ctx, pgstore.IncidentInput{
		TenantID:      w.tenantID,
		SourceEventID: source,
		Kind:          "other",
		Severity:      "high",
		Domain:        eff.provider,
		Subject:       eff.provider,
		Title:         "Outbound mail to " + eff.provider + " rerouted to fallback relay (transient 4xx defers)",
		Detail:        detail,
	}); err != nil {
		w.log.Error("open failover incident", "provider", eff.provider, "err", err)
	}
}
