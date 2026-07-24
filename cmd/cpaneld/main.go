// Command cpaneld synchronises cPanel/WHM server account data into MX Sentinel
// and periodically pushes per-account email metrics to connected WHMCS installations.
//
// Sync loop  (every tick): query servers whose sync_interval has elapsed, pull
// accounts + domains from each WHM API, and upsert into Postgres.
//
// WHMCS push loop (every tick): query connections whose push window has elapsed,
// aggregate ClickHouse metrics for the period, and push billable-item records to
// the WHMCS API.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/zezekim/mxsentinel/internal/config"
	cpanelpkg "github.com/zezekim/mxsentinel/internal/cpanel"
	"github.com/zezekim/mxsentinel/internal/crypto"
	"github.com/zezekim/mxsentinel/internal/obs"
	"github.com/zezekim/mxsentinel/internal/report"
	chstore "github.com/zezekim/mxsentinel/internal/store/clickhouse"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
	whmpkg "github.com/zezekim/mxsentinel/internal/whmcs"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "cpaneld:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Getenv("MXS_CONFIG"))
	if err != nil {
		return err
	}
	log := obs.NewLogger("cpaneld", cfg.LogLevel)

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

	enc, encrypted, err := crypto.NewEncryptor(cfg.Integration.EncryptionKey)
	if err != nil {
		return err
	}
	if !encrypted {
		log.Warn("cpaneld: running in passthrough mode — credentials stored as plaintext; set MXS_ENCRYPTION_KEY for production")
	}

	s := &syncer{log: log, pg: pg, ch: ch, enc: enc}

	srv.SetReady(true)
	log.Info("cpaneld started")

	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	// Run once immediately on startup, then on every tick.
	s.doSync(ctx)
	s.doWhmcsPush(ctx)

	for {
		select {
		case <-ctx.Done():
			log.Info("cpaneld shutting down")
			return nil
		case <-ticker.C:
			s.doSync(ctx)
			s.doWhmcsPush(ctx)
		}
	}
}

// syncer holds shared dependencies for the sync and push workers.
type syncer struct {
	log *slog.Logger
	pg  *pgstore.Store
	ch  *chstore.Store
	enc *crypto.Encryptor
}

// doSync queries every cPanel server whose sync interval has elapsed and performs
// a full account+domain sync for each one.
func (s *syncer) doSync(ctx context.Context) {
	servers, err := s.pg.GetServersNeedingSync(ctx)
	if err != nil {
		s.log.Error("cpaneld: get servers needing sync", "err", err)
		return
	}
	if len(servers) == 0 {
		return
	}
	s.log.Info("cpaneld: syncing servers", "count", len(servers))

	for _, srv := range servers {
		srv := srv // capture loop variable
		plainToken, err := s.enc.Open(srv.APIToken)
		if err != nil {
			s.log.Error("cpaneld: decrypt api_token", "server_id", srv.ID, "err", err)
			continue
		}

		client, err := cpanelpkg.New(cpanelpkg.Config{
			Hostname:  srv.Hostname,
			Port:      srv.Port,
			Username:  srv.Username,
			APIToken:  plainToken,
			VerifySSL: srv.VerifySSL,
		})
		if err != nil {
			s.log.Error("cpaneld: create cpanel client", "server_id", srv.ID, "err", err)
			continue
		}

		syncer := cpanelpkg.NewSyncer(client, s.pg, s.log.With("server_id", srv.ID))
		if err := syncer.Run(ctx, srv.ID); err != nil {
			s.log.Error("cpaneld: sync failed", "server_id", srv.ID, "err", err)
		} else {
			s.log.Info("cpaneld: sync complete", "server_id", srv.ID)
		}
	}
}

// doWhmcsPush queries every WHMCS connection whose push window has elapsed,
// fetches ClickHouse metrics for the relevant period, and pushes them to WHMCS.
func (s *syncer) doWhmcsPush(ctx context.Context) {
	connections, err := s.pg.GetDueWhmcsConnections(ctx)
	if err != nil {
		s.log.Error("cpaneld: get due whmcs connections", "err", err)
		return
	}
	if len(connections) == 0 {
		return
	}
	s.log.Info("cpaneld: pushing whmcs connections", "count", len(connections))

	for _, conn := range connections {
		conn := conn // capture loop variable
		if err := s.pushWhmcsConnection(ctx, conn); err != nil {
			s.log.Error("cpaneld: whmcs push failed", "connection_id", conn.ID, "err", err)
		}
	}
}

// pushWhmcsConnection handles the full push lifecycle for one WHMCS connection.
func (s *syncer) pushWhmcsConnection(ctx context.Context, conn pgstore.WhmcsConnection) error {
	log := s.log.With("connection_id", conn.ID)

	// Determine billing period boundaries.
	now := time.Now().UTC()
	var periodStart time.Time
	switch conn.PushFrequency {
	case "weekly":
		periodStart = now.Add(-7 * 24 * time.Hour)
	default: // "daily" and anything else
		periodStart = now.Add(-24 * time.Hour)
	}
	periodEnd := now

	// Decrypt the WHMCS API secret.
	plainSecret, err := s.enc.Open(conn.APISecret)
	if err != nil {
		return fmt.Errorf("decrypt api_secret: %w", err)
	}

	whmcsClient, err := whmpkg.New(whmpkg.Config{
		APIURL:        conn.APIURL,
		APIIdentifier: conn.APIIdentifier,
		APISecret:     plainSecret,
	})
	if err != nil {
		return fmt.Errorf("create whmcs client: %w", err)
	}

	// Collect domain → account maps for all cPanel servers belonging to this tenant.
	allDomainMaps, err := s.pg.GetAllDomainMaps(ctx, conn.TenantID)
	if err != nil {
		return fmt.Errorf("get all domain maps: %w", err)
	}

	// Flatten into a single domain → username map for AggregateByAccount, and
	// build the full domain list for the ClickHouse query.
	flatDomainToUsername := make(map[string]string)
	// domain → CpanelAccount (for owner email + primary domain lookup)
	flatDomainToAccount := make(map[string]pgstore.CpanelAccount)
	for _, domainMap := range allDomainMaps {
		for domain, acct := range domainMap {
			flatDomainToUsername[domain] = acct.Username
			flatDomainToAccount[domain] = acct
		}
	}

	allDomains := make([]string, 0, len(flatDomainToUsername))
	for d := range flatDomainToUsername {
		allDomains = append(allDomains, d)
	}

	// Query ClickHouse for per-domain metrics over the period.
	domainMetrics, err := s.ch.MetricsByDomain(ctx, conn.TenantID, allDomains, periodStart, periodEnd)
	if err != nil {
		log.Error("cpaneld: query clickhouse metrics", "err", err)
		// Record failure and bail — do not mark as pushed.
		_ = s.pg.RecordWhmcsPush(ctx, conn.ID, 0, "error", err.Error(), periodStart, periodEnd)
		return fmt.Errorf("query clickhouse metrics: %w", err)
	}

	// Aggregate by cPanel account username.
	byAccount := chstore.AggregateByAccount(domainMetrics, flatDomainToUsername)

	// Build AccountMetrics slice for PushMetrics.
	accountMetrics := make([]whmpkg.AccountMetrics, 0, len(byAccount))
	for username, agg := range byAccount {
		// Find representative account record for owner email + primary domain.
		// Walk allDomainMaps to locate the account whose username matches.
		var ownerEmail, primaryDomain string
		for _, domainMap := range allDomainMaps {
			for _, acct := range domainMap {
				if acct.Username == username {
					ownerEmail = acct.OwnerEmail
					primaryDomain = acct.PrimaryDomain
					break
				}
			}
			if ownerEmail != "" || primaryDomain != "" {
				break
			}
		}

		// Build the full deliverability report block for this account's primary domain, attached
		// to the WHMCS client as a copy-paste-ready note. Best-effort — the billable-item push
		// still happens if the report can't be built.
		var reportText string
		if primaryDomain != "" {
			if rep, rerr := report.Build(ctx, s.ch, s.pg, conn.TenantID, primaryDomain, periodStart, periodEnd); rerr == nil {
				reportText = rep.Text()
			} else {
				s.log.Warn("build deliverability report for whmcs note", "domain", primaryDomain, "err", rerr)
			}
		}

		accountMetrics = append(accountMetrics, whmpkg.AccountMetrics{
			CpanelUsername: username,
			PrimaryDomain:  primaryDomain,
			OwnerEmail:     ownerEmail,
			Delivered:      int64(agg.Delivered),
			Bounced:        int64(agg.Bounced),
			Rejected:       int64(agg.Rejected),
			Deferred:       int64(agg.Deferred),
			Total:          int64(agg.Total),
			PeriodStart:    periodStart,
			PeriodEnd:      periodEnd,
			ReportText:     reportText,
		})
	}

	// Push to WHMCS.
	pushed, pushErr := whmcsClient.PushMetrics(ctx, accountMetrics)

	status := "ok"
	errDetail := ""
	if pushErr != nil {
		status = "error"
		errDetail = pushErr.Error()
		log.Error("cpaneld: whmcs push partial failure", "pushed", pushed, "err", pushErr)
	} else {
		log.Info("cpaneld: whmcs push complete", "accounts_pushed", pushed,
			"period_start", periodStart.Format(time.RFC3339),
			"period_end", periodEnd.Format(time.RFC3339))
	}

	// Record push log entry.
	if err := s.pg.RecordWhmcsPush(ctx, conn.ID, pushed, status, errDetail, periodStart, periodEnd); err != nil {
		log.Error("cpaneld: record whmcs push log", "err", err)
	}

	// Advance last_pushed_at so this connection isn't selected again until next window.
	if err := s.pg.MarkWhmcsPushed(ctx, conn.ID); err != nil {
		log.Error("cpaneld: mark whmcs pushed", "err", err)
	}

	if pushErr != nil {
		return pushErr
	}
	return nil
}
