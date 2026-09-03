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
	"strings"
	"syscall"
	"time"

	"github.com/zezekim/mxsentinel/internal/api"
	"github.com/zezekim/mxsentinel/internal/auth"
	"github.com/zezekim/mxsentinel/internal/config"
	"github.com/zezekim/mxsentinel/internal/crypto"
	dnsx "github.com/zezekim/mxsentinel/internal/dns"
	"github.com/zezekim/mxsentinel/internal/obs"
	"github.com/zezekim/mxsentinel/internal/ratelimit"
	chstore "github.com/zezekim/mxsentinel/internal/store/clickhouse"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
	"github.com/zezekim/mxsentinel/internal/store/redisstore"
)

func main() {
	addr := flag.String("addr", ":8080", "API listen address")
	corsOrigin := flag.String("cors-origin", "*", "Access-Control-Allow-Origin value")
	rateLimit := flag.Int("rate-limit", 600, "max requests per tenant per minute (0 disables)")
	flag.Parse()

	if err := run(*addr, *corsOrigin, *rateLimit); err != nil {
		fmt.Fprintln(os.Stderr, "apid:", err)
		os.Exit(1)
	}
}

func run(addr, corsOrigin string, rateLimit int) error {
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

	// Redis powers user sessions (login) and, optionally, a shared rate-limit counter
	// (so multiple apid instances enforce one limit). Best-effort: if Redis is down,
	// login is disabled and rate limiting falls back to in-process.
	var sessions auth.SessionStore
	var counter ratelimit.Counter = ratelimit.NewMemCounter()
	if rs, rerr := redisstore.New(ctx, cfg.Redis); rerr != nil {
		log.Warn("redis unavailable: user login disabled, in-memory rate limiter", "err", rerr)
	} else {
		defer func() { _ = rs.Close() }()
		sessions = auth.NewRedisSessions(rs.Client)
		counter = rs.RateCounter()
	}

	var limiter api.Limiter
	if rateLimit > 0 {
		limiter = ratelimit.New(counter, rateLimit, time.Minute)
	}

	resolver := dnsx.NewSystemResolver(5 * time.Second)
	apiSrv := api.New(pg, ch, resolver, log, corsOrigin, limiter, sessions)

	// Wire credential encryptor for integrations (cPanel/WHMCS).
	enc, encrypted, err := crypto.NewEncryptor(cfg.Integration.EncryptionKey)
	if err != nil {
		return fmt.Errorf("integration encryptor: %w", err)
	}
	if !encrypted {
		log.Warn("MXS_ENCRYPTION_KEY not set — integration credentials stored as plaintext")
	}
	apiSrv = apiSrv.WithEncryptor(enc)

	// Overlay the dashboard-managed NL-analytics tool cap (Settings → Delivery & data tuning)
	// over MXS_NLQUERY_MAX_TOOLS; the dashboard value wins when set. Resolved once at startup
	// (restart to pick up a change). A read error is logged and treated as "not set".
	if tenantID := strings.TrimSpace(os.Getenv("RELAY_TENANT_ID")); tenantID != "" {
		if t, terr := pg.GetDeliveryTuning(ctx, tenantID); terr != nil {
			log.Warn("read dashboard delivery tuning; using env config", "err", terr)
		} else if t.NL.MaxTools > 0 {
			apiSrv = apiSrv.WithNLMaxTools(t.NL.MaxTools)
			log.Info("nlquery max-tools overridden from dashboard", "max_tools", t.NL.MaxTools)
		}
	}

	// Public origin for shareable message-trace links (e.g. https://sentinel.squidix.net).
	// When unset, the API returns relative /trace/<token> paths and the dashboard composes its
	// own origin.
	if base := os.Getenv("MXS_PUBLIC_BASE_URL"); base != "" {
		apiSrv = apiSrv.WithPublicBaseURL(base)
		log.Info("public trace links enabled", "base_url", base)
	}

	// Viewer-facing hostname masking. On the white-label domain a viewer must not be able
	// to tell who operates the relay, but the provider's hostnames run right through the
	// telemetry, so apid rewrites them to stable aliases on the way out rather than
	// blanking the data (internal/api/mask.go). Unset MXS_VIEWER_MASK_DOMAINS = disabled.
	if raw := os.Getenv("MXS_VIEWER_MASK_DOMAINS"); strings.TrimSpace(raw) != "" {
		base := os.Getenv("MXS_VIEWER_MASK_BASE")
		if base == "" {
			base = "example.net"
		}
		masker, merr := api.NewMasker(aliasAdapter{pg}, log, base, strings.FieldsFunc(raw,
			func(r rune) bool { return r == ',' || r == ' ' }))
		if merr != nil {
			return fmt.Errorf("viewer mask: %w", merr)
		}
		if werr := masker.Warm(ctx); werr != nil {
			// A cold cache is not fatal — aliases are re-read per host on demand — but it
			// means the first response for each host pays a database round-trip.
			log.Warn("viewer mask: could not preload aliases", "err", werr)
		}
		apiSrv = apiSrv.WithViewerMask(masker).WithPrimaryHost(os.Getenv("MXS_DOMAIN"))
		log.Info("viewer hostname masking enabled",
			"alias_base", base, "suffixes", raw, "primary_host", os.Getenv("MXS_DOMAIN"))
	}

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

// aliasAdapter bridges the Postgres store's row type to the api package's alias interface,
// so internal/api stays testable against a fake instead of a live database.
type aliasAdapter struct{ pg *pgstore.Store }

func (a aliasAdapter) ListViewerAliases(ctx context.Context) ([]api.AliasRow, error) {
	rows, err := a.pg.ListViewerAliases(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]api.AliasRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, api.AliasRow{RealHost: r.RealHost, Seq: r.Seq})
	}
	return out, nil
}

func (a aliasAdapter) AssignViewerAlias(ctx context.Context, host string) (int, error) {
	return a.pg.AssignViewerAlias(ctx, host)
}
