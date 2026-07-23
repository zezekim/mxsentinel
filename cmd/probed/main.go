// Command probed is the synthetic SMTP prober. On an interval it actively connects to each
// configured relay SMTP endpoint (ports 25/465/587) and records health: TCP connect latency,
// SMTP banner, EHLO capabilities, STARTTLS / implicit-TLS negotiation, TLS certificate expiry
// and chain validity, AUTH advertisement, and optional greylisting behaviour.
//
// Results are written to Postgres (current status + recent history) and ClickHouse (dense
// latency/uptime history). When a probe fails or a certificate nears expiry, probed emits an
// incident-eligible reputation event onto the bus (consumed by incidentd). Recording is the
// primary job: bus and ClickHouse are best-effort so probing never depends on them.
//
// This mirrors cmd/repd (daemon + ticker + obs scaffolding). See internal/smtpprobe and
// docs/smtp-probing.md.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/zezekim/mxsentinel/internal/config"
	"github.com/zezekim/mxsentinel/internal/events"
	"github.com/zezekim/mxsentinel/internal/obs"
	"github.com/zezekim/mxsentinel/internal/smtpprobe"
	chstore "github.com/zezekim/mxsentinel/internal/store/clickhouse"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
)

// cooldownFor returns how long to suppress repeat events of a given signal kind so a
// persistent failure/expiry doesn't open a new incident every tick.
func cooldownFor(key string) time.Duration {
	if strings.HasSuffix(key, "cert_expiring") {
		return 12 * time.Hour
	}
	return 30 * time.Minute
}

func main() {
	intervalFlag := flag.Duration("interval", 0, "override probe interval (0 = use MXS_PROBE_INTERVAL/default)")
	flag.Parse()

	if err := run(*intervalFlag); err != nil {
		fmt.Fprintln(os.Stderr, "probed:", err)
		os.Exit(1)
	}
}

func run(intervalOverride time.Duration) error {
	cfg, err := config.Load(os.Getenv("MXS_CONFIG"))
	if err != nil {
		return err
	}
	log := obs.NewLogger("probed", cfg.LogLevel)

	pcfg := smtpprobe.LoadConfig()
	interval := pcfg.Interval
	if intervalOverride > 0 {
		interval = intervalOverride
	}

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

	// ClickHouse is best-effort: probing/recording to Postgres must not depend on it.
	var ch *chstore.Store
	if c, cherr := chstore.New(ctx, cfg.ClickHouse); cherr != nil {
		log.Warn("clickhouse unavailable; probe history will be Postgres-only", "err", cherr)
	} else {
		ch = c
		defer ch.Close()
	}

	// Bus is best-effort: a probe failure is still recorded even if we can't publish an event.
	var bus *events.Bus
	if validator, verr := events.NewValidator(); verr != nil {
		log.Warn("event validator init failed; events disabled", "err", verr)
	} else if b, berr := events.Connect(cfg.NATS.URL, "probed", validator, log); berr != nil {
		log.Warn("nats unavailable; probe events disabled", "err", berr)
	} else {
		bus = b
		defer bus.Close()
		if eerr := bus.EnsureStreams(ctx); eerr != nil {
			log.Warn("ensure streams failed; probe events disabled", "err", eerr)
			bus = nil
		}
	}
	if pcfg.TenantID == "" {
		log.Warn("no MXS_PROBE_TENANT_ID/RELAY_TENANT_ID set; probe events will not be emitted (results still recorded)")
	}

	prober := &smtpprobe.Prober{
		Dialer:            &net.Dialer{},
		TLS:               smtpprobe.NewTLSHandshaker(pcfg.TLSInsecure),
		Now:               time.Now,
		EHLOName:          pcfg.EHLOName,
		ConnectTimeout:    pcfg.ConnectTimeout,
		CommandTimeout:    pcfg.CommandTimeout,
		CertWarnThreshold: pcfg.CertWarnThreshold,
		CheckResponse:     pcfg.CheckResponse,
	}

	w := &worker{
		log: log, pg: pg, ch: ch, bus: bus, prober: prober,
		endpoints: pcfg.Endpoints, tenantID: pcfg.TenantID,
		lastEvent: map[string]time.Time{},
	}

	// Best-effort: record the configured targets so the dashboard shows them pre-first-probe.
	for _, ep := range w.endpoints {
		if terr := pg.UpsertSMTPProbeTarget(ctx, ep.Label(), ep.Host, ep.Port, string(ep.Mode)); terr != nil {
			log.Warn("register probe target", "endpoint", ep.Label(), "err", terr)
		}
	}

	srv.SetReady(true)
	log.Info("probed started", "interval", interval.String(), "endpoints", len(w.endpoints))
	if len(w.endpoints) == 0 {
		log.Warn("no probe endpoints configured; set MXS_PROBE_ENDPOINTS or MXS_PROBE_HOST")
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	w.scan(ctx)
	for {
		select {
		case <-ctx.Done():
			log.Info("probed shutting down")
			return nil
		case <-ticker.C:
			w.scan(ctx)
		}
	}
}

type worker struct {
	log       *slog.Logger
	pg        *pgstore.Store
	ch        *chstore.Store
	bus       *events.Bus
	prober    *smtpprobe.Prober
	endpoints []smtpprobe.Endpoint
	tenantID  string
	lastEvent map[string]time.Time
}

func (w *worker) scan(ctx context.Context) {
	var chRows []chstore.SMTPProbeRow
	for _, ep := range w.endpoints {
		if ctx.Err() != nil {
			return
		}
		res := w.prober.Probe(ctx, ep)

		if err := w.pg.InsertSMTPProbeResult(ctx, toPGRow(res)); err != nil {
			w.log.Error("record probe result", "endpoint", ep.Label(), "err", err)
		}
		chRows = append(chRows, toCHRow(res))

		if res.OK {
			w.log.Info("probe ok", "endpoint", ep.Label(), "latency_ms", res.LatencyMS,
				"starttls", res.STARTTLSOffered, "auth", res.AuthAdvertised, "cert_expiring", res.CertExpiring())
		} else {
			w.log.Warn("probe failed", "endpoint", ep.Label(), "stage", res.Stage, "err", res.Error)
		}

		w.maybeEmit(ctx, res)
	}

	if w.ch != nil && len(chRows) > 0 {
		if err := w.ch.InsertSMTPProbeResults(ctx, chRows); err != nil {
			w.log.Warn("write probe history to clickhouse", "err", err)
		}
	}
}

// maybeEmit publishes an incident-eligible event for a failing probe or an expiring cert,
// honouring a per-signal cooldown so a persistent condition doesn't flood incidents.
func (w *worker) maybeEmit(ctx context.Context, res smtpprobe.ProbeResult) {
	if w.bus == nil || w.tenantID == "" {
		return
	}
	sig, ok := smtpprobe.DeriveSignal(res)
	if !ok {
		return
	}
	now := time.Now()
	if last, seen := w.lastEvent[sig.Key]; seen && now.Sub(last) < cooldownFor(sig.Key) {
		return
	}

	ev, err := events.NewEnvelope(sig.EventType, w.tenantID, "probed", sig.Correlation, now, sig.Payload)
	if err != nil {
		w.log.Error("build probe event", "endpoint", res.Endpoint.Label(), "err", err)
		return
	}
	if err := w.bus.Publish(ctx, ev); err != nil {
		w.log.Error("publish probe event", "endpoint", res.Endpoint.Label(), "err", err)
		return
	}
	w.lastEvent[sig.Key] = now
	w.log.Info("probe signal emitted", "endpoint", res.Endpoint.Label(),
		"kind", sig.Key, "severity", sig.Payload.Severity)
}

// --- mapping helpers --------------------------------------------------------

func toPGRow(r smtpprobe.ProbeResult) pgstore.SMTPProbeResult {
	row := pgstore.SMTPProbeResult{
		Endpoint:        r.Endpoint.Label(),
		Host:            r.Endpoint.Host,
		Port:            r.Endpoint.Port,
		Mode:            string(r.Endpoint.Mode),
		OK:              r.OK,
		Stage:           r.Stage,
		Error:           r.Error,
		LatencyMS:       r.LatencyMS,
		Banner:          r.Banner,
		STARTTLSOffered: r.STARTTLSOffered,
		AuthAdvertised:  r.AuthAdvertised,
		AuthMechs:       r.AuthMechs,
		Greylisting:     r.Greylisting,
		ResponseCode:    r.ResponseCode,
		ProbedAt:        r.ProbedAt,
	}
	if row.AuthMechs == nil {
		row.AuthMechs = []string{}
	}
	if r.TLS != nil && r.TLS.Negotiated {
		row.TLSNegotiated = true
		row.TLSVersion = r.TLS.Version
		row.TLSCipher = r.TLS.Cipher
		row.TLSChainValid = r.TLS.Cert.ChainValid
		row.CertSubject = r.TLS.Cert.Subject
		row.CertIssuer = r.TLS.Cert.Issuer
		row.CertExpiring = r.TLS.Cert.Expiring
		if !r.TLS.Cert.NotAfter.IsZero() {
			na := r.TLS.Cert.NotAfter
			row.CertNotAfter = &na
			d := r.TLS.Cert.DaysUntilExpiry
			row.CertDaysLeft = &d
		}
	}
	return row
}

func toCHRow(r smtpprobe.ProbeResult) chstore.SMTPProbeRow {
	row := chstore.SMTPProbeRow{
		ProbedAt:     r.ProbedAt,
		Endpoint:     r.Endpoint.Label(),
		Host:         r.Endpoint.Host,
		Port:         uint16(r.Endpoint.Port),
		Mode:         string(r.Endpoint.Mode),
		OK:           b2u8(r.OK),
		Stage:        r.Stage,
		Error:        r.Error,
		LatencyMS:    uint32(clampNonNeg(r.LatencyMS)),
		Greylisting:  b2u8(r.Greylisting),
		ResponseCode: uint16(r.ResponseCode),
	}
	if r.TLS != nil && r.TLS.Negotiated {
		row.TLSNegotiated = 1
		row.TLSVersion = r.TLS.Version
		row.TLSChainValid = b2u8(r.TLS.Cert.ChainValid)
		row.CertDaysLeft = int32(r.TLS.Cert.DaysUntilExpiry)
		row.CertNotAfter = r.TLS.Cert.NotAfter
		row.CertExpiring = b2u8(r.TLS.Cert.Expiring)
	}
	return row
}

func b2u8(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}

func clampNonNeg(n int64) int64 {
	if n < 0 {
		return 0
	}
	return n
}
