// Command abused is the outbound-abuse guard. It consumes SMTP telemetry from the bus,
// keeps a rolling per-authenticated-user window of reputation-harming bounces (recipients
// rejecting mail as spam/blocklisted), and when an account crosses the threshold it
// disables that SMTP submission user (Dovecot blocks the login on the next auth) and opens
// an incident. This is the containment layer that complements rspamd's inline filtering:
// rspamd stops spam it recognizes; abused amputates an account that real receivers are
// already rejecting, before the shared IP pool's reputation is burned.
//
// Detection is event-driven and in-memory (single relay node), so a ClickHouse-less,
// real-time signal — see docs/deploy-relay.md §9.9.
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

	"github.com/zezekim/mxsentinel/internal/config"
	"github.com/zezekim/mxsentinel/internal/events"
	"github.com/zezekim/mxsentinel/internal/obs"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
	"github.com/zezekim/mxsentinel/pkg/contracts"
)

// Thresholds (flag-tunable). An account trips when, within the rolling window, recipients
// reject its mail as spam/blocklisted/reputation either in absolute count or as a fraction
// of its volume. Delivered mail and transient deferrals never count against it, so a
// legitimate high-volume sender isn't suspended for sending a lot.
var (
	windowDur  = flag.Duration("window", time.Hour, "rolling window for per-user abuse accounting")
	minVolume  = flag.Int("min-volume", 50, "minimum messages in the window before the rate trigger applies")
	abuseRate  = flag.Float64("abuse-rate", 0.30, "fraction of windowed messages bounced as spam/block/reputation that trips suspension")
	abuseCount = flag.Int("abuse-count", 25, "absolute count of spam/block/reputation bounces that trips suspension regardless of rate")
	pruneEvery = flag.Duration("prune", 10*time.Minute, "how often to drop idle per-user windows")
)

func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "abused:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Getenv("MXS_CONFIG"))
	if err != nil {
		return err
	}
	log := obs.NewLogger("abused", cfg.LogLevel)

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
	bus, err := events.Connect(cfg.NATS.URL, "abused", validator, log)
	if err != nil {
		return err
	}
	defer bus.Close()
	if err := bus.EnsureStreams(ctx); err != nil {
		return err
	}

	w := &worker{log: log, pg: pg, users: map[string]*userWindow{}}

	cc, err := bus.Consume(ctx, events.ConsumeOptions{
		Stream: "SMTP", Durable: "abused", FilterSubject: "mxs.smtp.>",
	}, w.onSMTP)
	if err != nil {
		return err
	}
	defer cc.Stop()

	srv.SetReady(true)
	log.Info("abused started", "window", windowDur.String(), "abuse_rate", *abuseRate, "abuse_count", *abuseCount, "min_volume", *minVolume)

	ticker := time.NewTicker(*pruneEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Info("abused shutting down")
			return nil
		case <-ticker.C:
			w.prune(time.Now())
		}
	}
}

type sample struct {
	t     time.Time
	abuse bool
}

type userWindow struct {
	tenant  string
	samples []sample
}

type worker struct {
	log *slog.Logger
	pg  *pgstore.Store

	mu    sync.Mutex
	users map[string]*userWindow
}

// onSMTP folds one SMTP event into the sending user's rolling window and trips suspension
// if the abuse thresholds are crossed.
func (w *worker) onSMTP(ctx context.Context, ev *contracts.Envelope) error {
	var p contracts.SMTPPayload
	if err := ev.DecodePayload(&p); err != nil {
		return nil // malformed: drop, don't redeliver forever
	}
	if p.SASLUsername == "" {
		return nil // not attributable to an authenticated SMTP account
	}
	abuse := p.Outcome == "bounced" && isReputationBounce(p.BounceClass)

	now := time.Now()
	w.mu.Lock()
	uw := w.users[p.SASLUsername]
	if uw == nil {
		uw = &userWindow{}
		w.users[p.SASLUsername] = uw
	}
	uw.tenant = ev.TenantID
	cutoff := now.Add(-*windowDur)
	kept := uw.samples[:0]
	for _, s := range uw.samples {
		if s.t.After(cutoff) {
			kept = append(kept, s)
		}
	}
	kept = append(kept, sample{t: now, abuse: abuse})
	uw.samples = kept
	total, abuseN := len(kept), 0
	for _, s := range kept {
		if s.abuse {
			abuseN++
		}
	}
	trip := abuseN >= *abuseCount || (total >= *minVolume && float64(abuseN)/float64(total) >= *abuseRate)
	w.mu.Unlock()

	if trip {
		w.suspend(ctx, ev, p, total, abuseN)
	}
	return nil
}

// suspend disables the user (idempotently) and opens an incident on the first transition.
func (w *worker) suspend(ctx context.Context, ev *contracts.Envelope, p contracts.SMTPPayload, total, abuseN int) {
	tenantID, suspended, err := w.pg.DisableSMTPUserByUsername(ctx, p.SASLUsername)
	if err != nil {
		w.log.Error("disable smtp user", "user", p.SASLUsername, "err", err)
		return
	}
	// The account is disabled now (or already was) — drop its window either way.
	w.mu.Lock()
	delete(w.users, p.SASLUsername)
	w.mu.Unlock()
	if !suspended {
		return // already disabled: don't reopen an incident
	}

	rate := 0.0
	if total > 0 {
		rate = float64(abuseN) / float64(total)
	}
	w.log.Warn("auto-suspended SMTP user (outbound abuse)",
		"user", p.SASLUsername, "tenant", tenantID, "messages", total, "abuse_bounces", abuseN, "rate", rate)

	detail, _ := json.Marshal(map[string]any{
		"username":      p.SASLUsername,
		"window":        windowDur.String(),
		"messages":      total,
		"abuse_bounces": abuseN,
		"abuse_rate":    rate,
		"reason":        "recipients rejected this account's mail as spam/blocklisted above threshold",
	})
	if _, _, err := w.pg.InsertIncident(ctx, pgstore.IncidentInput{
		TenantID:      tenantID,
		SourceEventID: ev.EventID,
		Kind:          "other",
		Severity:      "critical",
		Domain:        p.FromDomain,
		Subject:       p.SASLUsername,
		Title:         "SMTP user auto-suspended (outbound spam/abuse)",
		Detail:        detail,
	}); err != nil {
		w.log.Error("open abuse incident", "user", p.SASLUsername, "err", err)
	}
}

// prune drops idle per-user windows so memory tracks only recently-active senders.
func (w *worker) prune(now time.Time) {
	cutoff := now.Add(-*windowDur)
	w.mu.Lock()
	defer w.mu.Unlock()
	for user, uw := range w.users {
		kept := uw.samples[:0]
		for _, s := range uw.samples {
			if s.t.After(cutoff) {
				kept = append(kept, s)
			}
		}
		if len(kept) == 0 {
			delete(w.users, user)
		} else {
			uw.samples = kept
		}
	}
}

func isReputationBounce(class string) bool {
	switch class {
	case "spam", "block", "reputation":
		return true
	default:
		return false
	}
}
