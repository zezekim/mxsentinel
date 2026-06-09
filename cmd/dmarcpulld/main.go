// Command dmarcpulld pulls DMARC aggregate report data from dmarc.squidix.org
// and writes it into MX Sentinel's Postgres + ClickHouse stores on a schedule.
// Configure via MXS_DMARCP_* env vars or the dmarcp: section in mxsentinel.yaml.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/zezekim/mxsentinel/internal/config"
	"github.com/zezekim/mxsentinel/internal/dmarcpull"
	"github.com/zezekim/mxsentinel/internal/obs"
	chstore "github.com/zezekim/mxsentinel/internal/store/clickhouse"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "dmarcpulld:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Getenv("MXS_CONFIG"))
	if err != nil {
		return err
	}
	log := obs.NewLogger("dmarcpulld", cfg.LogLevel)

	dp := cfg.DmarcPull
	if dp.BaseURL == "" {
		return errors.New("MXS_DMARCP_BASEURL is required")
	}
	if dp.APIKey == "" {
		return errors.New("MXS_DMARCP_APIKEY is required")
	}
	if dp.TenantID == "" {
		return errors.New("MXS_DMARCP_TENANTID is required")
	}
	interval := time.Duration(dp.Interval) * time.Second
	if interval <= 0 {
		interval = time.Hour
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

	ch, err := chstore.New(ctx, cfg.ClickHouse)
	if err != nil {
		return err
	}
	defer ch.Close()

	client := dmarcpull.NewClient(dp.BaseURL, dp.APIKey)
	if err := client.Ping(ctx); err != nil {
		log.Warn("dmarcpulld: remote API ping failed; will retry on first tick", "err", err)
	}

	syncer := dmarcpull.NewSyncer(client, pg, ch, dp.TenantID, dp.LookbackDays, log)

	srv.SetReady(true)
	log.Info("dmarcpulld started", "interval", interval.String(), "tenant", dp.TenantID)

	// Run immediately, then on each tick.
	doSync(ctx, syncer, log)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Info("dmarcpulld shutting down")
			return nil
		case <-ticker.C:
			doSync(ctx, syncer, log)
		}
	}
}

func doSync(ctx context.Context, syncer *dmarcpull.Syncer, log interface{ Error(string, ...any) }) {
	if err := syncer.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("dmarcpulld: sync failed", "err", err)
	}
}
