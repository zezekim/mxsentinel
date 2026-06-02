// Command dnsd is the DNS Intelligence validator daemon. It polls monitored domains,
// snapshots their DNS posture (writing a new snapshot only when it changes), and publishes
// dns.changed / dns.validation_failed events. See docs/phase-1-plan.md WS3.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/zezekim/mxsentinel/internal/config"
	dnsx "github.com/zezekim/mxsentinel/internal/dns"
	"github.com/zezekim/mxsentinel/internal/events"
	"github.com/zezekim/mxsentinel/internal/obs"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
	"github.com/zezekim/mxsentinel/pkg/contracts"
)

func main() {
	interval := flag.Duration("interval", time.Minute, "base poll interval for monitored domains")
	flag.Parse()

	if err := run(*interval); err != nil {
		fmt.Fprintln(os.Stderr, "dnsd:", err)
		os.Exit(1)
	}
}

func run(interval time.Duration) error {
	cfg, err := config.Load(os.Getenv("MXS_CONFIG"))
	if err != nil {
		return err
	}
	log := obs.NewLogger("dnsd", cfg.LogLevel)

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
	bus, err := events.Connect(cfg.NATS.URL, "dnsd", validator, log)
	if err != nil {
		return err
	}
	defer bus.Close()
	if err := bus.EnsureStreams(ctx); err != nil {
		return err
	}

	resolver := dnsx.NewSystemResolver(5 * time.Second)
	w := &worker{log: log, pg: pg, bus: bus, resolver: resolver}
	srv.SetReady(true)
	log.Info("dnsd started", "interval", interval.String())

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	w.pollAll(ctx) // initial pass
	for {
		select {
		case <-ctx.Done():
			log.Info("dnsd shutting down")
			return nil
		case <-ticker.C:
			w.pollAll(ctx)
		}
	}
}

type worker struct {
	log      *slog.Logger
	pg       *pgstore.Store
	bus      *events.Bus
	resolver dnsx.Resolver
}

func (w *worker) pollAll(ctx context.Context) {
	domains, err := w.pg.ListMonitoredDomains(ctx)
	if err != nil {
		w.log.Error("list domains failed", "err", err)
		return
	}
	for _, d := range domains {
		if ctx.Err() != nil {
			return
		}
		// small jitter to avoid hammering resolvers in lockstep
		time.Sleep(time.Duration(rand.IntN(250)) * time.Millisecond)
		w.poll(ctx, d)
	}
}

func (w *worker) poll(ctx context.Context, d pgstore.MonitoredDomain) {
	prior, err := w.pg.LatestSnapshotChecksum(ctx, d.ID)
	if err != nil {
		w.log.Error("read prior checksum", "domain", d.Name, "err", err)
		return
	}

	snap, err := dnsx.Inspect(ctx, w.resolver, d.Name, dnsx.Options{})
	if err != nil {
		w.log.Error("inspect failed", "domain", d.Name, "err", err)
		return
	}

	if err := w.pg.MarkDomainChecked(ctx, d.ID); err != nil {
		w.log.Warn("mark checked failed", "domain", d.Name, "err", err)
	}

	changed := snap.Checksum != prior
	first := prior == ""
	if !changed && !first {
		return // nothing new; do not re-snapshot or re-alert
	}

	snapID, err := w.pg.InsertSnapshot(ctx, d.ID, snap.StateJSON, snap.Checksum, snap.Healthy, snap.Findings)
	if err != nil {
		w.log.Error("insert snapshot failed", "domain", d.Name, "err", err)
		return
	}
	w.log.Info("snapshot written", "domain", d.Name, "snapshot", snapID,
		"changed", changed, "healthy", snap.Healthy, "findings", len(snap.Findings))

	if changed && !first {
		w.publishChanged(ctx, d, snapID, snap)
	}
	if hasAlertable(snap.Findings) {
		w.publishValidationFailed(ctx, d, snapID, snap)
	}
}

func (w *worker) publishChanged(ctx context.Context, d pgstore.MonitoredDomain, snapID string, snap dnsx.Snapshot) {
	ev, err := events.NewEnvelope(contracts.EventDNSChanged, d.TenantID, "dnsd",
		contracts.Correlation{Domain: d.Name}, time.Now(),
		contracts.DNSPayload{Domain: d.Name, DomainID: d.ID, SnapshotID: snapID, Checksum: snap.Checksum})
	if err != nil {
		w.log.Error("build dns.changed", "err", err)
		return
	}
	if err := w.bus.Publish(ctx, ev); err != nil {
		w.log.Error("publish dns.changed", "domain", d.Name, "err", err)
	}
}

func (w *worker) publishValidationFailed(ctx context.Context, d pgstore.MonitoredDomain, snapID string, snap dnsx.Snapshot) {
	ev, err := events.NewEnvelope(contracts.EventDNSValidationFailed, d.TenantID, "dnsd",
		contracts.Correlation{Domain: d.Name}, time.Now(),
		contracts.DNSPayload{
			Domain: d.Name, DomainID: d.ID, SnapshotID: snapID,
			Checksum: snap.Checksum, Findings: snap.Findings,
		})
	if err != nil {
		w.log.Error("build dns.validation_failed", "err", err)
		return
	}
	if err := w.bus.Publish(ctx, ev); err != nil {
		w.log.Error("publish dns.validation_failed", "domain", d.Name, "err", err)
	}
}

// hasAlertable reports whether any finding is warning-or-worse.
func hasAlertable(fs []contracts.DNSFinding) bool {
	for _, f := range fs {
		if strings.EqualFold(f.Severity, dnsx.SevWarning) || strings.EqualFold(f.Severity, dnsx.SevCritical) {
			return true
		}
	}
	return false
}
