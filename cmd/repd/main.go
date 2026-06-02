// Command repd is the reputation monitor. It periodically checks every registered sending
// IP (relay nodes + IP pools) against DNS blocklists and publishes a
// reputation.blacklist_hit event per (ip, zone) listing. incidentd persists these as
// incidents. See ARCHITECTURE.md §1 (reputation management) and Phase 2.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/zezekim/mxsentinel/internal/config"
	dnsx "github.com/zezekim/mxsentinel/internal/dns"
	"github.com/zezekim/mxsentinel/internal/events"
	"github.com/zezekim/mxsentinel/internal/obs"
	"github.com/zezekim/mxsentinel/internal/reputation"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
	"github.com/zezekim/mxsentinel/pkg/contracts"
)

const alertCooldown = 6 * time.Hour

func main() {
	interval := flag.Duration("interval", time.Hour, "how often to re-scan all IPs against DNSBLs")
	zonesFlag := flag.String("zones", "", "comma-separated DNSBL zones (default: built-in list)")
	flag.Parse()

	zones := reputation.DefaultZones
	if *zonesFlag != "" {
		zones = splitZones(*zonesFlag)
	}

	if err := run(*interval, zones); err != nil {
		fmt.Fprintln(os.Stderr, "repd:", err)
		os.Exit(1)
	}
}

func run(interval time.Duration, zones []string) error {
	cfg, err := config.Load(os.Getenv("MXS_CONFIG"))
	if err != nil {
		return err
	}
	log := obs.NewLogger("repd", cfg.LogLevel)

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
	bus, err := events.Connect(cfg.NATS.URL, "repd", validator, log)
	if err != nil {
		return err
	}
	defer bus.Close()
	if err := bus.EnsureStreams(ctx); err != nil {
		return err
	}

	w := &worker{
		log: log, pg: pg, bus: bus,
		resolver:  dnsx.NewSystemResolver(5 * time.Second),
		zones:     zones,
		lastAlert: map[string]time.Time{},
	}
	srv.SetReady(true)
	log.Info("repd started", "interval", interval.String(), "zones", zones)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	w.scan(ctx)
	for {
		select {
		case <-ctx.Done():
			log.Info("repd shutting down")
			return nil
		case <-ticker.C:
			w.scan(ctx)
		}
	}
}

type worker struct {
	log       *slog.Logger
	pg        *pgstore.Store
	bus       *events.Bus
	resolver  dnsx.Resolver
	zones     []string
	lastAlert map[string]time.Time
}

func (w *worker) scan(ctx context.Context) {
	targets, err := w.pg.ListReputationTargets(ctx)
	if err != nil {
		w.log.Error("list reputation targets", "err", err)
		return
	}
	for _, t := range targets {
		if ctx.Err() != nil {
			return
		}
		listings, err := reputation.Check(ctx, w.resolver, t.IP, w.zones)
		if err != nil {
			w.log.Warn("rbl check failed", "ip", t.IP, "err", err)
			continue
		}
		for _, l := range listings {
			w.alert(ctx, t, l)
		}
	}
}

func (w *worker) alert(ctx context.Context, t pgstore.ReputationTarget, l reputation.Listing) {
	key := t.TenantID + "|" + t.IP + "|" + l.Zone
	now := time.Now()
	if last, seen := w.lastAlert[key]; seen && now.Sub(last) < alertCooldown {
		return
	}

	ev, err := events.NewEnvelope(contracts.EventReputationBlacklistHit, t.TenantID, "repd",
		contracts.Correlation{RelayIP: t.IP}, now,
		contracts.ReputationPayload{
			Signal: "blacklist_hit", SubjectKind: "ip", Subject: t.IP,
			Severity: "critical", Source: l.Zone,
			Detail: map[string]any{
				"codes": l.Codes, "txt": l.TXT, "label": t.Label, "source_kind": t.Source,
			},
		})
	if err != nil {
		w.log.Error("build blacklist_hit", "err", err)
		return
	}
	if err := w.bus.Publish(ctx, ev); err != nil {
		w.log.Error("publish blacklist_hit", "ip", t.IP, "zone", l.Zone, "err", err)
		return
	}
	w.lastAlert[key] = now
	w.log.Info("blacklist hit", "ip", t.IP, "zone", l.Zone, "codes", l.Codes, "label", t.Label)
}

func splitZones(s string) []string {
	var out []string
	for _, z := range strings.Split(s, ",") {
		if z = strings.TrimSpace(z); z != "" {
			out = append(out, z)
		}
	}
	return out
}
