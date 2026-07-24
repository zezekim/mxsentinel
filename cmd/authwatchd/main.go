// Command authwatchd is the SASL credential-compromise detector. It consumes SMTP telemetry
// from the bus keyed by the authenticated submission user (SASLUsername) and maintains a
// rolling per-credential behavioral window. It scores compromise-shaped changes —
//
//  1. a burst of DISTINCT recipient domains in a short window (list-blasting),
//  2. a sharp spam/block/reputation bounce-rate spike on the credential,
//  3. volume far above the credential's recent norm (EWMA baseline),
//  4. (optional) off-hours concentration —
//
// combines them into a score, and when it crosses an env-tunable threshold it records a
// signal (credential_auth_signal) and opens a critical incident. Auto-locking the credential
// is OPT-IN (AUTHWATCH_AUTOLOCK=true) because on a shared relay one credential serves every
// client, so locking it is drastic (same caveat as cmd/abused -suspend-credential).
//
// SHARED-RELAY CAVEAT: with ONE SASL credential for hundreds of client domains, per-credential
// keying is coarse, and the classic "new geo/ASN login" anomaly is degenerate — there is one
// source IP, and the submitting client's IP is not even on the bus (SMTPPayload.RelayIP is the
// EGRESS node). The IP-geo path is gated behind authwatch.GeoLookup with a no-op default and is
// a documented follow-up (needs a telemetry extension to capture the smtpd client IP + a GeoIP
// DB). These behavioral signals are strongest in DEDICATED-SUBMISSION deployments. See docs.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/zezekim/mxsentinel/internal/authwatch"
	"github.com/zezekim/mxsentinel/internal/config"
	"github.com/zezekim/mxsentinel/internal/events"
	"github.com/zezekim/mxsentinel/internal/obs"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
	"github.com/zezekim/mxsentinel/pkg/contracts"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "authwatchd:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Getenv("MXS_CONFIG"))
	if err != nil {
		return err
	}
	log := obs.NewLogger("authwatchd", cfg.LogLevel)

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

	var pg *pgstore.Store
	if err := obs.Retry(ctx, log, "postgres", 30, 2*time.Second, func() error {
		pg, err = pgstore.New(ctx, cfg.Postgres)
		return err
	}); err != nil {
		return err
	}
	defer pg.Close()

	validator, err := events.NewValidator()
	if err != nil {
		return err
	}
	var bus *events.Bus
	if err := obs.Retry(ctx, log, "nats", 30, 2*time.Second, func() error {
		bus, err = events.Connect(cfg.NATS.URL, "authwatchd", validator, log)
		return err
	}); err != nil {
		return err
	}
	defer bus.Close()
	if err := bus.EnsureStreams(ctx); err != nil {
		return err
	}

	dcfg := configFromEnv(log)
	autolock := envBool("AUTHWATCH_AUTOLOCK", false)

	// Overlay dashboard-managed tuning (Postgres) over the env-loaded config. Precedence is
	// DASHBOARD > env > default: a value set via the Settings page wins, an unset (zero) value
	// leaves the env/default in place, and a DB read error is treated as "unset" so we never
	// blank a working env-configured detector. Scoped to RELAY_TENANT_ID (the single tenant this
	// relay's daemons run for); with no tenant set there is nothing to look up.
	if tenantID := strings.TrimSpace(os.Getenv("RELAY_TENANT_ID")); tenantID != "" {
		if t, err := pg.GetAbuseTuning(ctx, tenantID); err != nil {
			log.Warn("abuse tuning: read failed; using env/default", "tenant_id", tenantID, "err", err)
		} else {
			applyAuthwatchTuning(&dcfg, &autolock, t.Authwatch, log)
		}
	}

	w := &worker{
		log:      log,
		store:    authwatch.NewStore(pg.Pool),
		pg:       pg,
		det:      authwatch.NewDetector(dcfg),
		geo:      authwatch.NoopGeo{}, // shared-relay: client source IP not on the bus (see geo.go)
		autolock: autolock,
	}

	cc, err := bus.Consume(ctx, events.ConsumeOptions{
		Stream: "SMTP", Durable: "authwatchd", FilterSubject: "mxs.smtp.>",
	}, w.onSMTP)
	if err != nil {
		return err
	}
	defer cc.Stop()

	srv.SetReady(true)
	log.Info("authwatchd started",
		"window", dcfg.Window.String(), "threshold", dcfg.Threshold,
		"distinct_rcpt_threshold", dcfg.DistinctRcptThreshold,
		"bounce_rate", dcfg.BounceRate, "volume_factor", dcfg.VolumeFactor,
		"autolock", autolock)

	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Info("authwatchd shutting down")
			return nil
		case <-ticker.C:
			w.det.Prune(time.Now())
		}
	}
}

type worker struct {
	log      *slog.Logger
	store    *authwatch.Store
	pg       *pgstore.Store
	det      *authwatch.Detector
	geo      authwatch.GeoLookup
	autolock bool
}

// onSMTP folds one SMTP event into its credential's window and acts on a trip.
func (w *worker) onSMTP(ctx context.Context, ev *contracts.Envelope) error {
	var p contracts.SMTPPayload
	if err := ev.DecodePayload(&p); err != nil {
		return nil // malformed: drop, don't redeliver forever
	}
	if p.SASLUsername == "" {
		return nil // not attributable to a credential (e.g. inbound or unauthenticated)
	}

	det, tenant, domain, tripped := w.det.Observe(authwatch.Observation{
		TenantID:        ev.TenantID,
		SASLUsername:    p.SASLUsername,
		FromDomain:      p.FromDomain,
		RecipientDomain: p.RecipientDomain,
		Abuse:           p.Outcome == "bounced" && isReputationBounce(p.BounceClass),
		At:              time.Now(),
	})
	if !tripped {
		return nil
	}
	w.act(ctx, ev, p.SASLUsername, tenant, domain, det)
	return nil
}

// act records the signal, opens a critical incident, and (only when AUTHWATCH_AUTOLOCK=true)
// locks the credential.
func (w *worker) act(ctx context.Context, ev *contracts.Envelope, user, tenant, domain string, det *authwatch.TripDetail) {
	w.log.Warn("possible credential compromise",
		"sasl_username", user, "tenant", tenant, "score", det.Score,
		"contributors", det.Contributors, "messages", det.Messages,
		"distinct_recipients", det.DistinctRcpts, "bounce_rate", det.BounceRate)

	detail, _ := json.Marshal(det)

	if err := w.store.InsertSignal(ctx, user, authwatch.SignalComposite, detail); err != nil {
		w.log.Error("insert credential auth signal", "user", user, "err", err)
	}

	if _, _, err := w.pg.InsertIncident(ctx, pgstore.IncidentInput{
		TenantID:      tenant,
		SourceEventID: ev.EventID,
		Kind:          "other",
		Severity:      "critical",
		Domain:        domain,
		Subject:       user,
		Title:         "Possible credential compromise: " + user,
		Detail:        detail,
	}); err != nil {
		w.log.Error("open credential-compromise incident", "user", user, "err", err)
	}

	// Auto-lock is opt-in: a shared relay credential serves every client, so locking it is
	// drastic. When enabled, disable the credential (Dovecot stops accepting its logins) and
	// record the lock for the Auth Security UI.
	if w.autolock {
		reason := "auto-locked by authwatchd: possible credential compromise (score " +
			strconv.FormatFloat(det.Score, 'f', 2, 64) + ")"
		if _, suspended, err := w.pg.DisableSMTPUserByUsername(ctx, user); err != nil {
			w.log.Error("disable smtp user", "user", user, "err", err)
		} else if suspended {
			w.log.Warn("auto-locked SMTP credential", "user", user)
		}
		if err := w.store.SetLock(ctx, user, true, reason); err != nil {
			w.log.Error("record credential lock", "user", user, "err", err)
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

// configFromEnv builds the detector config from AUTHWATCH_* env vars, falling back to
// conservative defaults. Bad values log a warning and keep the default.
func configFromEnv(log *slog.Logger) authwatch.Config {
	c := authwatch.DefaultConfig()
	c.Window = envDuration("AUTHWATCH_WINDOW", c.Window, log)
	c.Cooldown = envDuration("AUTHWATCH_COOLDOWN", c.Cooldown, log)
	c.DistinctRcptThreshold = envInt("AUTHWATCH_DISTINCT_RCPT", c.DistinctRcptThreshold, log)
	c.MinVolume = envInt("AUTHWATCH_MIN_VOLUME", c.MinVolume, log)
	c.BounceRate = envFloat("AUTHWATCH_BOUNCE_RATE", c.BounceRate, log)
	c.VolumeFactor = envFloat("AUTHWATCH_VOLUME_FACTOR", c.VolumeFactor, log)
	c.VolumeFloor = envInt("AUTHWATCH_VOLUME_FLOOR", c.VolumeFloor, log)
	c.OffHoursStart = envInt("AUTHWATCH_OFFHOURS_START", c.OffHoursStart, log)
	c.OffHoursEnd = envInt("AUTHWATCH_OFFHOURS_END", c.OffHoursEnd, log)
	c.OffHoursRate = envFloat("AUTHWATCH_OFFHOURS_RATE", c.OffHoursRate, log)
	c.OffHoursWeight = envFloat("AUTHWATCH_OFFHOURS_WEIGHT", c.OffHoursWeight, log)
	c.Threshold = envFloat("AUTHWATCH_THRESHOLD", c.Threshold, log)
	return c
}

// applyAuthwatchTuning overlays non-zero dashboard-managed values onto the detector config (and
// the opt-in auto-lock switch). A zero value means "unset" — left untouched so the env/default
// stands. Durations arrive as integer seconds. It logs how many fields it changed for
// auditability at startup.
func applyAuthwatchTuning(c *authwatch.Config, autolock *bool, t pgstore.AuthwatchTuning, log *slog.Logger) {
	n := 0
	if t.Threshold > 0 {
		c.Threshold = t.Threshold
		n++
	}
	if t.WindowSecs > 0 {
		c.Window = time.Duration(t.WindowSecs) * time.Second
		n++
	}
	if t.CooldownSecs > 0 {
		c.Cooldown = time.Duration(t.CooldownSecs) * time.Second
		n++
	}
	if t.MinVolume > 0 {
		c.MinVolume = t.MinVolume
		n++
	}
	if t.DistinctRcpt > 0 {
		c.DistinctRcptThreshold = t.DistinctRcpt
		n++
	}
	if t.BounceRate > 0 {
		c.BounceRate = t.BounceRate
		n++
	}
	if t.VolumeFactor > 0 {
		c.VolumeFactor = t.VolumeFactor
		n++
	}
	if t.VolumeFloor > 0 {
		c.VolumeFloor = t.VolumeFloor
		n++
	}
	if t.OffHoursStart > 0 {
		c.OffHoursStart = t.OffHoursStart
		n++
	}
	if t.OffHoursEnd > 0 {
		c.OffHoursEnd = t.OffHoursEnd
		n++
	}
	if t.OffHoursRate > 0 {
		c.OffHoursRate = t.OffHoursRate
		n++
	}
	if t.OffHoursWeight > 0 {
		c.OffHoursWeight = t.OffHoursWeight
		n++
	}
	// Auto-lock is a bool where false = "unset" per the group convention: the dashboard can turn
	// it ON but not force it OFF (leave the checkbox unchecked to defer to AUTHWATCH_AUTOLOCK).
	if t.Autolock {
		*autolock = true
		n++
	}
	if n > 0 {
		log.Info("applied dashboard abuse tuning (authwatch)", "fields", n)
	}
}

func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func envInt(key string, def int, log *slog.Logger) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		log.Warn("invalid int env; using default", "key", key, "value", v, "default", def)
		return def
	}
	return n
}

func envFloat(key string, def float64, log *slog.Logger) float64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		log.Warn("invalid float env; using default", "key", key, "value", v, "default", def)
		return def
	}
	return f
}

func envDuration(key string, def time.Duration, log *slog.Logger) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		log.Warn("invalid duration env; using default", "key", key, "value", v, "default", def.String())
		return def
	}
	return d
}
