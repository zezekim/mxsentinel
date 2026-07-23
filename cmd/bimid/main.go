// Command bimid is the BIMI / VMC readiness daemon. It polls monitored domains, resolves each
// domain's default._bimi TXT record, validates the referenced SVG Tiny P/S logo and any VMC
// certificate, cross-checks DMARC enforcement from the latest DNS snapshot, and writes a new
// bimi_snapshots row whenever the readiness assessment changes. It mirrors cmd/dnsd's poll /
// snapshot-on-change pattern. See docs/bimi.md.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/zezekim/mxsentinel/internal/bimi"
	"github.com/zezekim/mxsentinel/internal/config"
	dnsx "github.com/zezekim/mxsentinel/internal/dns"
	"github.com/zezekim/mxsentinel/internal/obs"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
)

func main() {
	var interval time.Duration
	flag.DurationVar(&interval, "interval", 0, "poll interval (0 = MXS_BIMI_INTERVAL / default 1h)")
	flag.Parse()

	if err := run(interval); err != nil {
		fmt.Fprintln(os.Stderr, "bimid:", err)
		os.Exit(1)
	}
}

func run(interval time.Duration) error {
	cfg, err := config.Load(os.Getenv("MXS_CONFIG"))
	if err != nil {
		return err
	}
	bcfg := bimi.LoadConfig()
	if interval <= 0 {
		interval = bcfg.Interval
	}

	log := obs.NewLogger("bimid", cfg.LogLevel)

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

	w := &worker{
		log:      log,
		pg:       pg,
		resolver: dnsx.NewSystemResolver(5 * time.Second),
		fetcher:  bimi.NewHTTPFetcher(bcfg.FetchTimeout),
	}
	srv.SetReady(true)
	log.Info("bimid started", "interval", interval.String())

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	w.pollAll(ctx) // initial pass
	for {
		select {
		case <-ctx.Done():
			log.Info("bimid shutting down")
			return nil
		case <-ticker.C:
			w.pollAll(ctx)
		}
	}
}

type worker struct {
	log      *slog.Logger
	pg       *pgstore.Store
	resolver bimi.Resolver
	fetcher  bimi.Fetcher
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
		// small jitter so we don't hammer logo/VMC hosts in lockstep
		time.Sleep(time.Duration(rand.IntN(250)) * time.Millisecond)
		w.poll(ctx, d)
	}
}

func (w *worker) poll(ctx context.Context, d pgstore.MonitoredDomain) {
	dmarcRecord, err := w.pg.LatestDMARCRecord(ctx, d.ID)
	if err != nil {
		w.log.Error("read dmarc record", "tenant_id", d.TenantID, "domain", d.Name, "err", err)
		return
	}

	rep, err := bimi.Inspect(ctx, w.resolver, w.fetcher, d.Name, dmarcRecord)
	if err != nil {
		w.log.Error("bimi inspect failed", "tenant_id", d.TenantID, "domain", d.Name, "err", err)
		return
	}

	prior, hasPrior, err := w.pg.LatestBIMISnapshot(ctx, d.ID)
	if err != nil {
		w.log.Error("read prior bimi snapshot", "tenant_id", d.TenantID, "domain", d.Name, "err", err)
		return
	}
	if hasPrior && !changed(prior, rep) {
		return // nothing new
	}

	checklist, _ := json.Marshal(rep.Checklist)
	snap := pgstore.BIMISnapshot{
		TenantID:      d.TenantID,
		DomainID:      d.ID,
		Record:        rep.Record.Raw,
		LogoURL:       rep.LogoURL,
		VMCURL:        rep.VMCURL,
		VMCExpiry:     rep.VMCExpiry,
		DMARCEnforced: rep.DMARCEnforced,
		Readiness:     rep.Readiness,
		Checklist:     checklist,
	}
	id, err := w.pg.InsertBIMISnapshot(ctx, snap)
	if err != nil {
		w.log.Error("insert bimi snapshot", "tenant_id", d.TenantID, "domain", d.Name, "err", err)
		return
	}
	w.log.Info("bimi snapshot written", "tenant_id", d.TenantID, "domain", d.Name,
		"snapshot", id, "readiness", rep.Readiness, "dmarc_enforced", rep.DMARCEnforced)
}

// changed reports whether the new assessment differs materially from the prior snapshot.
func changed(prior pgstore.BIMISnapshot, rep bimi.Report) bool {
	if prior.Readiness != rep.Readiness ||
		prior.Record != rep.Record.Raw ||
		prior.LogoURL != rep.LogoURL ||
		prior.VMCURL != rep.VMCURL ||
		prior.DMARCEnforced != rep.DMARCEnforced {
		return true
	}
	return !sameTime(prior.VMCExpiry, rep.VMCExpiry)
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}
