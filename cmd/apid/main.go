// Command apid serves the MX Sentinel REST API (v1): domain health, DNS drift timeline,
// message explorer, and DMARC reports. See internal/api and docs/api-v1.md (WS6).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/zezekim/mxsentinel/internal/api"
	"github.com/zezekim/mxsentinel/internal/config"
	dnsx "github.com/zezekim/mxsentinel/internal/dns"
	"github.com/zezekim/mxsentinel/internal/obs"
	chstore "github.com/zezekim/mxsentinel/internal/store/clickhouse"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
)

func main() {
	addr := flag.String("addr", ":8080", "API listen address")
	corsOrigin := flag.String("cors-origin", "*", "Access-Control-Allow-Origin value")
	flag.Parse()

	if err := run(*addr, *corsOrigin); err != nil {
		fmt.Fprintln(os.Stderr, "apid:", err)
		os.Exit(1)
	}
}

func run(addr, corsOrigin string) error {
	cfg, err := config.Load(os.Getenv("MXS_CONFIG"))
	if err != nil {
		return err
	}
	log := obs.NewLogger("apid", cfg.LogLevel)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	metrics := obs.NewMetrics()
	obsSrv := obs.NewServer(cfg.HTTPAddr, metrics, log) // /healthz + /metrics
	obsSrv.Start()
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = obsSrv.Shutdown(sctx)
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

	resolver := dnsx.NewSystemResolver(5 * time.Second)
	apiSrv := api.New(pg, ch, resolver, log, corsOrigin)

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           apiSrv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Info("apid listening", "addr", addr, "cors_origin", corsOrigin)
		obsSrv.SetReady(true)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("api server failed", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	log.Info("apid shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return httpSrv.Shutdown(sctx)
}
