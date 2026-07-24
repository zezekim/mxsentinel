// Command tlsrptd is the transport-security monitor. It does two things on a single loop:
//
//  1. MTA-STS validation: on an interval it polls every monitored domain, resolves the
//     _mta-sts TXT record, fetches + parses the published policy, checks the TLS certs of
//     the policy's MX hosts, and writes a new mtasts_snapshots row only when the state
//     changes (drift). On a warning-or-worse finding it publishes a dns.validation_failed
//     event (reusing the DNS event contract, category "mta_sts").
//
//  2. TLS-RPT ingestion: it watches a drop directory for TLS-RPT JSON reports
//     (.json/.json.gz), archives each raw report to object storage, parses it, writes a
//     pointer row (Postgres) + per-MTA detail rows (ClickHouse), and quarantines malformed
//     reports instead of crashing. Structurally identical to dmarcd.
//
// See docs/tls-reporting.md.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/zezekim/mxsentinel/internal/config"
	dnsx "github.com/zezekim/mxsentinel/internal/dns"
	"github.com/zezekim/mxsentinel/internal/events"
	"github.com/zezekim/mxsentinel/internal/mtasts"
	"github.com/zezekim/mxsentinel/internal/obs"
	chstore "github.com/zezekim/mxsentinel/internal/store/clickhouse"
	"github.com/zezekim/mxsentinel/internal/store/objectstore"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
	"github.com/zezekim/mxsentinel/internal/tlsrpt"
	"github.com/zezekim/mxsentinel/pkg/contracts"
)

const (
	subProcessed  = "processed"
	subQuarantine = "quarantine"
)

func main() {
	var (
		dir      = flag.String("dir", "", "TLS-RPT report drop directory (default MXS_TLSRPT_DIR)")
		interval = flag.Duration("interval", 0, "MTA-STS re-check interval (default MXS_MTASTS_INTERVAL or 1h)")
	)
	flag.Parse()

	if err := run(*dir, *interval); err != nil {
		fmt.Fprintln(os.Stderr, "tlsrptd:", err)
		os.Exit(1)
	}
}

func run(dirFlag string, intervalFlag time.Duration) error {
	cfg, err := config.Load(os.Getenv("MXS_CONFIG"))
	if err != nil {
		return err
	}
	fcfg := mtasts.LoadConfig()
	if dirFlag != "" {
		fcfg.DropDir = dirFlag
	}
	if intervalFlag > 0 {
		fcfg.PollInterval = intervalFlag
	}

	log := obs.NewLogger("tlsrptd", cfg.LogLevel)

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

	// Dashboard-managed tuning wins over env/flag when the relay tenant is known.
	applyTuningOverlay(ctx, pg, log, &fcfg)

	ch, err := chstore.New(ctx, cfg.ClickHouse)
	if err != nil {
		return err
	}
	defer ch.Close()

	obj, err := objectstore.New(ctx, cfg.ObjectStore)
	if err != nil {
		return err
	}
	if err := obj.EnsureBucket(ctx, cfg.ObjectStore.Region); err != nil {
		return err
	}

	// Event bus is best-effort: MTA-STS/TLS-RPT monitoring must not fail because NATS is
	// down. Snapshots and reports are still persisted; only drift events are skipped.
	var bus *events.Bus
	if validator, verr := events.NewValidator(); verr != nil {
		log.Warn("event validator init failed; drift events disabled", "err", verr)
	} else if b, berr := events.Connect(cfg.NATS.URL, "tlsrptd", validator, log); berr != nil {
		log.Warn("event bus connect failed; drift events disabled", "err", berr)
	} else {
		bus = b
		defer bus.Close()
		if serr := bus.EnsureStreams(ctx); serr != nil {
			log.Warn("ensure streams failed; drift events may be dropped", "err", serr)
		}
	}

	w := &worker{
		log:      log,
		pg:       pg,
		bus:      bus,
		resolver: dnsx.NewSystemResolver(fcfg.HTTPTimeout),
		fetcher:  mtasts.NewHTTPPolicyFetcher(fcfg.HTTPTimeout),
		certs:    mtasts.NewSMTPCertChecker(fcfg.CertTimeout),
		ing:      tlsrpt.NewIngestor(obj, pg, ch),
		warnDays: fcfg.CertWarnDays,
	}
	srv.SetReady(true)
	log.Info("tlsrptd started", "mta_sts_interval", fcfg.PollInterval.String(),
		"tlsrpt_dir", fcfg.DropDir, "tlsrpt_interval", fcfg.DropInterval.String())

	mtaTicker := time.NewTicker(fcfg.PollInterval)
	defer mtaTicker.Stop()
	dropTicker := time.NewTicker(fcfg.DropInterval)
	defer dropTicker.Stop()

	w.pollAllMTASTS(ctx)          // initial MTA-STS pass
	w.scanDrop(ctx, fcfg.DropDir) // initial drop-dir pass
	for {
		select {
		case <-ctx.Done():
			log.Info("tlsrptd shutting down")
			return nil
		case <-mtaTicker.C:
			w.pollAllMTASTS(ctx)
		case <-dropTicker.C:
			w.scanDrop(ctx, fcfg.DropDir)
		}
	}
}

// applyTuningOverlay overlays dashboard-managed monitoring tuning (stored per relay tenant)
// onto the env-derived MTA-STS / TLS-RPT config. Dashboard values win; unset (0) fields are
// left as-is. Precedence is dashboard > env > default. A DB read error is logged and treated
// as "not configured" so the daemon still starts on its env/defaults.
func applyTuningOverlay(ctx context.Context, pg *pgstore.Store, log *slog.Logger, fcfg *mtasts.Config) {
	tenant := strings.TrimSpace(os.Getenv("RELAY_TENANT_ID"))
	if tenant == "" {
		return
	}
	t, err := pg.GetMonitoringTuning(ctx, tenant)
	if err != nil {
		log.Warn("read monitoring tuning; using env/defaults", "err", err)
		return
	}
	if t.TLSRPT.IntervalSecs > 0 {
		fcfg.DropInterval = time.Duration(t.TLSRPT.IntervalSecs) * time.Second
	}
	if t.MTASTS.IntervalSecs > 0 {
		fcfg.PollInterval = time.Duration(t.MTASTS.IntervalSecs) * time.Second
	}
	if t.MTASTS.CertWarnDays > 0 {
		fcfg.CertWarnDays = t.MTASTS.CertWarnDays
	}
	if t.MTASTS.CertTimeoutSecs > 0 {
		fcfg.CertTimeout = time.Duration(t.MTASTS.CertTimeoutSecs) * time.Second
	}
	if t.MTASTS.HTTPTimeoutSecs > 0 {
		fcfg.HTTPTimeout = time.Duration(t.MTASTS.HTTPTimeoutSecs) * time.Second
	}
	log.Info("applied dashboard monitoring tuning", "tenant_id", tenant)
}

type worker struct {
	log      *slog.Logger
	pg       *pgstore.Store
	bus      *events.Bus
	resolver dnsx.Resolver
	fetcher  mtasts.PolicyFetcher
	certs    mtasts.CertChecker
	ing      *tlsrpt.Ingestor
	warnDays int
}

// ─── MTA-STS polling ────────────────────────────────────────────────────────────

func (w *worker) pollAllMTASTS(ctx context.Context) {
	domains, err := w.pg.ListMonitoredDomains(ctx)
	if err != nil {
		w.log.Error("list domains failed", "err", err)
		return
	}
	for _, d := range domains {
		if ctx.Err() != nil {
			return
		}
		time.Sleep(time.Duration(rand.IntN(250)) * time.Millisecond) // small jitter
		w.pollMTASTS(ctx, d)
	}
}

func (w *worker) pollMTASTS(ctx context.Context, d pgstore.MonitoredDomain) {
	prior, err := w.pg.LatestMTASTSChecksum(ctx, d.ID)
	if err != nil {
		w.log.Error("read prior mtasts checksum", "domain", d.Name, "err", err)
		return
	}
	snap, err := mtasts.Inspect(ctx, mtasts.Deps{
		Resolver: w.resolver, Fetcher: w.fetcher, CertChecker: w.certs,
	}, d.Name, mtasts.Options{CertWarnDays: w.warnDays})
	if err != nil {
		w.log.Error("mtasts inspect failed", "domain", d.Name, "err", err)
		return
	}

	changed := snap.Checksum != prior
	first := prior == ""
	if !changed && !first {
		return // nothing new
	}

	var certExpiry *time.Time
	if !snap.CertExpiry.IsZero() {
		ce := snap.CertExpiry
		certExpiry = &ce
	}
	snapID, err := w.pg.InsertMTASTSSnapshot(ctx, pgstore.MTASTSSnapshot{
		TenantID:   d.TenantID,
		DomainID:   d.ID,
		DomainName: d.Name,
		Mode:       string(snap.State.Mode),
		MaxAge:     snap.State.MaxAge,
		MXHosts:    snap.State.MX,
		Checksum:   snap.Checksum,
		CertExpiry: certExpiry,
		IsHealthy:  snap.Healthy,
		StateJSON:  snap.StateJSON,
		Findings:   snap.Findings,
	})
	if err != nil {
		w.log.Error("insert mtasts snapshot failed", "domain", d.Name, "err", err)
		return
	}
	w.log.Info("mtasts snapshot written", "domain", d.Name, "snapshot", snapID,
		"tenant_id", d.TenantID, "changed", changed, "mode", snap.State.Mode,
		"healthy", snap.Healthy, "findings", len(snap.Findings))

	if mtasts.HasAlertable(snap.Findings) {
		w.publishValidationFailed(ctx, d, snapID, snap)
	}
}

func (w *worker) publishValidationFailed(ctx context.Context, d pgstore.MonitoredDomain, snapID string, snap mtasts.Snapshot) {
	if w.bus == nil {
		return
	}
	ev, err := events.NewEnvelope(contracts.EventDNSValidationFailed, d.TenantID, "tlsrptd",
		contracts.Correlation{Domain: d.Name}, time.Now(),
		contracts.DNSPayload{
			Domain: d.Name, DomainID: d.ID, SnapshotID: snapID,
			Checksum: snap.Checksum, Findings: snap.Findings,
			ChangedCategories: []string{mtasts.CatMTASTS},
		})
	if err != nil {
		w.log.Error("build mta-sts validation_failed", "err", err)
		return
	}
	if err := w.bus.Publish(ctx, ev); err != nil {
		w.log.Error("publish mta-sts validation_failed", "domain", d.Name, "err", err)
	}
}

// ─── TLS-RPT drop-dir ingestion ─────────────────────────────────────────────────

func (w *worker) scanDrop(ctx context.Context, dir string) {
	if dir == "" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		w.log.Error("read tlsrpt drop dir", "dir", dir, "err", err)
		return
	}
	for _, e := range entries {
		if ctx.Err() != nil {
			return
		}
		if e.IsDir() || !isReportFile(e.Name()) {
			continue
		}
		w.processFile(ctx, dir, e.Name())
	}
}

func (w *worker) processFile(ctx context.Context, dir, name string) {
	full := filepath.Join(dir, name)
	data, err := os.ReadFile(full)
	if err != nil {
		w.log.Error("read tlsrpt report", "file", name, "err", err)
		return
	}
	res, err := w.ing.IngestFile(ctx, name, data)
	if err != nil {
		// Infrastructure error — leave the file in place to retry.
		w.log.Error("tlsrpt ingest failed; will retry", "file", name, "err", err)
		return
	}
	dest := subProcessed
	if res.Status == tlsrpt.StatusQuarantined || res.Status == tlsrpt.StatusNoTenant {
		dest = subQuarantine
	}
	attrs := []any{"file", name, "status", string(res.Status), "report_id", res.ReportID,
		"domain", res.Domain, "records", res.Records}
	if res.Err != nil {
		attrs = append(attrs, "reason", res.Err.Error())
	}
	w.log.Info("processed tlsrpt report", attrs...)
	if mErr := moveTo(dir, dest, name); mErr != nil {
		w.log.Warn("move processed tlsrpt file", "file", name, "err", mErr)
	}
}

func moveTo(dir, sub, name string) error {
	dstDir := filepath.Join(dir, sub)
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}
	return os.Rename(filepath.Join(dir, name), filepath.Join(dstDir, name))
}

func isReportFile(name string) bool {
	n := strings.ToLower(name)
	return strings.HasSuffix(n, ".json") || strings.HasSuffix(n, ".json.gz") || strings.HasSuffix(n, ".gz")
}
