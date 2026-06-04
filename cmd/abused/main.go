// Command abused is the outbound-abuse guard. It consumes SMTP telemetry from the bus and
// keeps a rolling per-sending-domain window of reputation-harming bounces (recipients
// rejecting mail as spam/blocklisted). When a domain crosses the threshold it opens a
// critical incident so the offending client can be dealt with at the source — before the
// shared IP pool's reputation is burned. It complements rspamd's inline filtering: rspamd
// rate-limits and scores on the way out; abused flags a client whose mail real receivers
// are already rejecting.
//
// It keys on the SENDING DOMAIN, not the SASL login, on purpose: a shared hosting relay
// authenticates with one credential for hundreds of client domains, so abuse must be
// isolated per client. By default abused only alerts (opens an incident) — suspending the
// shared credential would take down every client. Set -suspend-credential only when each
// SMTP user maps to a single sender (dedicated-submission deployments).
//
// Detection is event-driven and in-memory (single relay node) — a ClickHouse-less,
// real-time signal. See docs/deploy-relay.md §9.9 / §11.
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

// Thresholds (flag-tunable). A sending domain trips when, within the rolling window,
// recipients reject its mail as spam/blocklisted/reputation either in absolute count or as
// a fraction of its volume. Delivered mail and transient deferrals never count, so a
// legitimate busy domain isn't flagged just for sending a lot.
var (
	windowDur   = flag.Duration("window", time.Hour, "rolling window for per-domain abuse accounting")
	minVolume   = flag.Int("min-volume", 50, "minimum messages in the window before the rate trigger applies")
	abuseRate   = flag.Float64("abuse-rate", 0.30, "fraction of windowed messages bounced as spam/block/reputation that trips an alert")
	abuseCount  = flag.Int("abuse-count", 25, "absolute count of spam/block/reputation bounces that trips an alert regardless of rate")
	cooldownDur = flag.Duration("incident-cooldown", 6*time.Hour, "minimum time between incidents for the same sending domain")
	pruneEvery  = flag.Duration("prune", 10*time.Minute, "how often to drop idle per-domain windows")
	suspendCred = flag.Bool("suspend-credential", false, "also disable the SMTP credential the abusive mail authenticated as — ONLY for dedicated per-sender logins; do NOT use with a shared relay credential")
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

	w := &worker{
		log: log, pg: pg,
		suspendCredential: *suspendCred,
		cooldown:          *cooldownDur,
		domains:           map[string]*domainWindow{},
		alerted:           map[string]time.Time{},
	}

	cc, err := bus.Consume(ctx, events.ConsumeOptions{
		Stream: "SMTP", Durable: "abused", FilterSubject: "mxs.smtp.>",
	}, w.onSMTP)
	if err != nil {
		return err
	}
	defer cc.Stop()

	srv.SetReady(true)
	log.Info("abused started", "window", windowDur.String(), "abuse_rate", *abuseRate,
		"abuse_count", *abuseCount, "min_volume", *minVolume, "suspend_credential", *suspendCred)

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

type domainWindow struct {
	tenant  string
	user    string // last SASL login seen for this domain (context for the incident)
	samples []sample
}

type worker struct {
	log *slog.Logger
	pg  *pgstore.Store

	suspendCredential bool
	cooldown          time.Duration

	mu      sync.Mutex
	domains map[string]*domainWindow
	alerted map[string]time.Time // sending domain -> last incident time (cooldown)
}

// onSMTP folds one SMTP event into its sending domain's rolling window and raises an alert
// when the abuse thresholds are crossed (deduped per domain by the cooldown).
func (w *worker) onSMTP(ctx context.Context, ev *contracts.Envelope) error {
	var p contracts.SMTPPayload
	if err := ev.DecodePayload(&p); err != nil {
		return nil // malformed: drop, don't redeliver forever
	}
	domain := p.FromDomain
	if domain == "" {
		return nil // not attributable to a sending domain
	}
	abuse := p.Outcome == "bounced" && isReputationBounce(p.BounceClass)

	now := time.Now()
	w.mu.Lock()
	dw := w.domains[domain]
	if dw == nil {
		dw = &domainWindow{}
		w.domains[domain] = dw
	}
	dw.tenant = ev.TenantID
	if p.SASLUsername != "" {
		dw.user = p.SASLUsername
	}
	cutoff := now.Add(-*windowDur)
	kept := dw.samples[:0]
	for _, s := range dw.samples {
		if s.t.After(cutoff) {
			kept = append(kept, s)
		}
	}
	kept = append(kept, sample{t: now, abuse: abuse})
	dw.samples = kept
	total, abuseN := len(kept), 0
	for _, s := range kept {
		if s.abuse {
			abuseN++
		}
	}
	trip := abuseN >= *abuseCount || (total >= *minVolume && float64(abuseN)/float64(total) >= *abuseRate)
	if trip {
		if last, seen := w.alerted[domain]; seen && now.Sub(last) < w.cooldown {
			trip = false // already alerted recently
		} else {
			w.alerted[domain] = now
		}
	}
	user, tenant := dw.user, dw.tenant
	w.mu.Unlock()

	if trip {
		w.alert(ctx, ev, domain, user, tenant, total, abuseN)
	}
	return nil
}

// alert opens a critical incident for the offending sending domain (and, only when
// explicitly enabled, disables the credential it came through).
func (w *worker) alert(ctx context.Context, ev *contracts.Envelope, domain, user, tenant string, total, abuseN int) {
	rate := 0.0
	if total > 0 {
		rate = float64(abuseN) / float64(total)
	}
	w.log.Warn("outbound abuse detected for sending domain",
		"domain", domain, "via_user", user, "tenant", tenant, "messages", total, "abuse_bounces", abuseN, "rate", rate)

	detail, _ := json.Marshal(map[string]any{
		"sending_domain": domain,
		"via_smtp_user":  user,
		"window":         windowDur.String(),
		"messages":       total,
		"abuse_bounces":  abuseN,
		"abuse_rate":     rate,
		"reason":         "recipients rejected this sending domain's mail as spam/blocklisted above threshold",
		"remediation":    "suspend or investigate the sending account at the hosting server",
	})
	if _, _, err := w.pg.InsertIncident(ctx, pgstore.IncidentInput{
		TenantID:      tenant,
		SourceEventID: ev.EventID,
		Kind:          "other",
		Severity:      "critical",
		Domain:        domain,
		Subject:       domain,
		Title:         "Outbound spam/abuse from sending domain " + domain,
		Detail:        detail,
	}); err != nil {
		w.log.Error("open abuse incident", "domain", domain, "err", err)
	}

	// Disabling the credential is opt-in: with a shared relay login it would take down
	// every client, so it's off by default (alert-only).
	if w.suspendCredential && user != "" {
		if _, suspended, err := w.pg.DisableSMTPUserByUsername(ctx, user); err != nil {
			w.log.Error("disable smtp user", "user", user, "err", err)
		} else if suspended {
			w.log.Warn("suspended SMTP credential", "user", user, "trigger_domain", domain)
		}
	}
}

// prune drops idle per-domain windows and stale cooldown entries so memory tracks only
// recently-active senders.
func (w *worker) prune(now time.Time) {
	cutoff := now.Add(-*windowDur)
	w.mu.Lock()
	defer w.mu.Unlock()
	for domain, dw := range w.domains {
		kept := dw.samples[:0]
		for _, s := range dw.samples {
			if s.t.After(cutoff) {
				kept = append(kept, s)
			}
		}
		if len(kept) == 0 {
			delete(w.domains, domain)
		} else {
			dw.samples = kept
		}
	}
	for domain, t := range w.alerted {
		if now.Sub(t) > w.cooldown {
			delete(w.alerted, domain)
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
