// Command sndsd is the Microsoft Outlook/Hotmail deliverability daemon — the mirror of
// cmd/fbld for the Gmail/Google half. It has two independent halves, either of which runs
// alone:
//
//  1. SNDS poll: on an interval it fetches Microsoft's Smart Network Data Services
//     automated-data CSV (keyed by MXS_SNDS_KEY), parses per-IP reputation (filter result,
//     complaint band, spam-trap hits), upserts each (IP, day) row to Postgres, streams a
//     long-horizon copy to ClickHouse when available, and opens a critical incident when an IP
//     is graded RED or shows trap hits. Skipped cleanly (logged once) when MXS_SNDS_KEY is
//     unset — exactly as fbld skips Postmaster without GOOGLE_POSTMASTER_TOKEN.
//
//  2. JMRP ingest: it watches a drop directory (MXS_JMRP_DIR) for Junk Mail Reporting Program
//     ARF complaint emails (same RFC 5965 format the Google FBL uses — parsed via
//     internal/fbl.ParseARF), attributes each to the sending domain/IP, upserts a per-day
//     complaint summary, and trips a critical incident when a sending domain crosses the 24h
//     threshold. Mirrors cmd/dmarcd + cmd/fbld's drop-directory pattern.
//
// Attribution: SNDS rows are keyed by the SENDING IP (resolved to its owning tenant from the
// relay/pool egress-IP inventory); JMRP complaints by SENDING DOMAIN (per-client on a shared
// relay), matching cmd/fbld and cmd/abused.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/zezekim/mxsentinel/internal/config"
	"github.com/zezekim/mxsentinel/internal/crypto"
	"github.com/zezekim/mxsentinel/internal/integrationsettings"
	"github.com/zezekim/mxsentinel/internal/obs"
	"github.com/zezekim/mxsentinel/internal/snds"
	chstore "github.com/zezekim/mxsentinel/internal/store/clickhouse"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
)

const (
	subProcessed  = "processed"
	subQuarantine = "quarantine"
)

func main() {
	var (
		dir       = flag.String("dir", "", "JMRP drop directory (default $MXS_JMRP_DIR or /jmrp-drop)")
		scanEvery = flag.Duration("scan-interval", 0, "JMRP drop-directory scan interval (default $MXS_JMRP_SCAN_INTERVAL or 30s)")
		sndsEvery = flag.Duration("snds-interval", 0, "SNDS CSV poll interval (default $MXS_SNDS_INTERVAL or 6h)")
	)
	flag.Parse()

	if err := run(*dir, *scanEvery, *sndsEvery); err != nil {
		fmt.Fprintln(os.Stderr, "sndsd:", err)
		os.Exit(1)
	}
}

func run(dirFlag string, scanFlag, sndsFlag time.Duration) error {
	cfg, err := config.Load(os.Getenv("MXS_CONFIG"))
	if err != nil {
		return err
	}
	log := obs.NewLogger("sndsd", cfg.LogLevel)

	sc := snds.LoadConfig()
	dir := sc.JMRPDir
	if dirFlag != "" {
		dir = dirFlag
	}
	scanInterval := sc.ScanInterval
	if scanFlag > 0 {
		scanInterval = scanFlag
	}
	sndsInterval := sc.Interval
	if sndsFlag > 0 {
		sndsInterval = sndsFlag
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

	var pg *pgstore.Store
	if err := obs.Retry(ctx, log, "postgres", 30, 2*time.Second, func() error {
		pg, err = pgstore.New(ctx, cfg.Postgres)
		return err
	}); err != nil {
		return err
	}
	defer pg.Close()

	// ClickHouse is optional: it only holds the long-horizon SNDS trend history. If it can't be
	// reached we log once and continue with Postgres alone — monitoring must never block on it.
	var ch *chstore.Store
	if c, cerr := chstore.New(ctx, cfg.ClickHouse); cerr != nil {
		log.Warn("clickhouse unavailable; SNDS history will be stored in postgres only", "err", cerr)
	} else {
		ch = c
		defer ch.Close()
	}

	// Overlay the dashboard-managed SNDS key (Settings → Providers & keys) over env; the
	// dashboard value wins when set. Applied at startup (restart to pick up a change).
	if tenantID := strings.TrimSpace(os.Getenv("RELAY_TENANT_ID")); tenantID != "" {
		enc, _, encErr := crypto.NewEncryptor(cfg.Integration.EncryptionKey)
		if encErr != nil {
			log.Warn("init encryptor for provider settings", "err", encErr)
		}
		rs := integrationsettings.Resolve(ctx, pg, enc, tenantID, log)
		sc.AccessKey = integrationsettings.Prefer(rs.SNDSKey, sc.AccessKey)

		// Overlay the dashboard-managed sndsd tuning (Settings → Delivery & data tuning) over env;
		// dashboard values win when set. A DB read error is logged and treated as "not set".
		if t, terr := pg.GetDeliveryTuning(ctx, tenantID); terr != nil {
			log.Warn("read dashboard delivery tuning; using env config", "err", terr)
		} else {
			if t.SNDS.IntervalSecs > 0 {
				sndsInterval = time.Duration(t.SNDS.IntervalSecs) * time.Second
			}
			if t.SNDS.JMRPScanIntervalSecs > 0 {
				scanInterval = time.Duration(t.SNDS.JMRPScanIntervalSecs) * time.Second
			}
			if t.SNDS.JMRPComplaintThreshold > 0 {
				sc.ComplaintThreshold = t.SNDS.JMRPComplaintThreshold
			}
		}
	}

	client, sndsEnabled := snds.NewClient(sc.AccessKey, sc.DataURL)
	if !sndsEnabled {
		log.Info("MXS_SNDS_KEY not set; skipping SNDS poll (JMRP complaint ingestion still active)")
	}

	d := &daemon{
		log:    log,
		pg:     pg,
		ch:     ch,
		client: client,
		rollup: snds.NewRollup(pg, log, sc.ComplaintThreshold),
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Warn("create JMRP drop dir", "dir", dir, "err", err)
	}

	srv.SetReady(true)
	log.Info("sndsd started",
		"jmrp_dir", dir, "scan_interval", scanInterval.String(),
		"snds_enabled", sndsEnabled, "snds_interval", sndsInterval.String(),
		"clickhouse_enabled", ch != nil, "complaint_threshold", d.rollup.Threshold())

	// Initial pass on startup, then on ticks.
	d.pollSNDS(ctx)
	d.scanJMRP(ctx, dir)

	sndsTick := time.NewTicker(sndsInterval)
	defer sndsTick.Stop()
	scanTick := time.NewTicker(scanInterval)
	defer scanTick.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info("sndsd shutting down")
			return nil
		case <-sndsTick.C:
			d.pollSNDS(ctx)
		case <-scanTick.C:
			d.scanJMRP(ctx, dir)
		}
	}
}

type daemon struct {
	log    *slog.Logger
	pg     *pgstore.Store
	ch     *chstore.Store // nil when ClickHouse is unavailable
	client *snds.Client   // nil when SNDS is disabled
	rollup *snds.Rollup
}

// pollSNDS fetches the SNDS CSV, stores each per-IP row for the tenant that owns the egress IP,
// and trips incidents on RED/trap-hit results. No-op when SNDS is disabled.
func (d *daemon) pollSNDS(ctx context.Context) {
	if d.client == nil {
		return
	}
	rows, err := d.client.FetchData(ctx)
	if err != nil {
		d.log.Warn("snds fetch failed; skipping this cycle", "err", err)
		return
	}
	ipTenants, err := d.ipTenantMap(ctx)
	if err != nil {
		d.log.Error("build ip->tenant map; skipping SNDS cycle", "err", err)
		return
	}

	day := today()
	var chBatch []chstore.SNDSRow
	stored, skipped := 0, 0
	for _, row := range rows {
		if ctx.Err() != nil {
			return
		}
		tenantID, ok := ipTenants[row.IP]
		if !ok {
			skipped++ // egress IP not attributable to a tenant — don't store an orphan row
			continue
		}
		start, end := timePtr(row.ActivityStart), timePtr(row.ActivityEnd)
		if err := d.pg.UpsertSNDSIPData(ctx, pgstore.SNDSIPData{
			TenantID:      tenantID,
			IP:            row.IP,
			DataDate:      row.DataDate,
			RcptCount:     int64(row.RcptCount),
			DataCount:     int64(row.DataCount),
			MsgRecipients: int64(row.MsgRecipients),
			FilterResult:  row.FilterResult,
			ComplaintBand: row.ComplaintBand,
			TrapHits:      row.TrapHits,
			SampleHELO:    row.SampleHELO,
			SampleFrom:    row.SampleFrom,
			ActivityStart: start,
			ActivityEnd:   end,
		}); err != nil {
			d.log.Error("store snds row", "ip", row.IP, "err", err)
			continue
		}
		stored++
		if d.ch != nil {
			chBatch = append(chBatch, chstore.SNDSRow{
				TenantID:      tenantID,
				IP:            row.IP,
				DataDate:      row.DataDate,
				RcptCount:     uint64(row.RcptCount),
				DataCount:     uint64(row.DataCount),
				MsgRecipients: uint64(row.MsgRecipients),
				FilterResult:  row.FilterResult,
				ComplaintBand: row.ComplaintBand,
				TrapHits:      uint32(row.TrapHits),
				SampleHELO:    row.SampleHELO,
				SampleFrom:    row.SampleFrom,
				FetchedAt:     time.Now().UTC(),
			})
		}
		d.rollup.OnFilterResult(ctx, tenantID, row.IP, row.FilterResult, row.TrapHits, day)
	}
	if d.ch != nil && len(chBatch) > 0 {
		if err := d.ch.InsertSNDSRows(ctx, chBatch); err != nil {
			d.log.Warn("store snds history in clickhouse", "err", err)
		}
	}
	d.log.Info("snds poll complete", "rows", len(rows), "stored", stored, "skipped_unattributed", skipped)
}

// ipTenantMap maps every tenant-attributable egress IP to its owning tenant, from the same
// relay/pool inventory repd scans.
func (d *daemon) ipTenantMap(ctx context.Context) (map[string]string, error) {
	targets, err := d.pg.ListReputationTargets(ctx)
	if err != nil {
		return nil, err
	}
	m := make(map[string]string, len(targets))
	for _, t := range targets {
		m[t.IP] = t.TenantID
	}
	return m, nil
}

func (d *daemon) scanJMRP(ctx context.Context, dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		d.log.Error("read JMRP drop dir", "dir", dir, "err", err)
		return
	}
	var ipTenants map[string]string // built lazily only when there's work
	for _, e := range entries {
		if ctx.Err() != nil {
			return
		}
		if e.IsDir() || !isComplaintFile(e.Name()) {
			continue
		}
		if ipTenants == nil {
			if ipTenants, err = d.ipTenantMap(ctx); err != nil {
				d.log.Error("build ip->tenant map for JMRP; skipping scan", "err", err)
				return
			}
		}
		d.processJMRP(ctx, dir, e.Name(), ipTenants)
	}
}

func (d *daemon) processJMRP(ctx context.Context, dir, name string, ipTenants map[string]string) {
	full := filepath.Join(dir, name)
	data, err := os.ReadFile(full)
	if err != nil {
		d.log.Error("read JMRP complaint", "file", name, "err", err)
		return
	}

	c, err := snds.ParseJMRP(data)
	if err != nil {
		d.log.Warn("malformed JMRP complaint; quarantining", "file", name, "err", err)
		if mErr := moveTo(dir, subQuarantine, name); mErr != nil {
			d.log.Warn("move quarantined file", "file", name, "err", mErr)
		}
		return
	}

	tenantID, ok := d.resolveJMRPTenant(ctx, c, ipTenants)
	if !ok {
		// Complaint about a domain/IP we don't attribute to a tenant — log so the operator can
		// register it, but don't store/incident an unattributable complaint. Archive it so the
		// dir doesn't grow unbounded.
		d.log.Warn("JMRP complaint for unregistered sender; skipping",
			"file", name, "sender_domain", c.SenderDomain, "sending_ip", c.SourceIP)
		if mErr := moveTo(dir, subQuarantine, name); mErr != nil {
			d.log.Warn("move unattributed file", "file", name, "err", mErr)
		}
		return
	}

	day := todayDate()
	if err := d.pg.UpsertJMRPComplaint(ctx, pgstore.JMRPComplaintInput{
		TenantID:     tenantID,
		SenderDomain: c.SenderDomain,
		SendingIP:    c.SourceIP,
		FeedbackType: c.FeedbackType,
		Provider:     c.Provider,
		Day:          day,
	}); err != nil {
		// Infrastructure error (DB) — leave the file in place to retry.
		d.log.Error("record JMRP complaint failed; will retry", "file", name, "err", err)
		return
	}

	d.log.Info("processed JMRP complaint",
		"file", name, "feedback_type", c.FeedbackType,
		"sender_domain", c.SenderDomain, "sending_ip", c.SourceIP)

	if c.SenderDomain != "" {
		if count, err := d.pg.JMRPComplaintCount24h(ctx, tenantID, c.SenderDomain); err != nil {
			d.log.Error("jmrp complaint count 24h", "domain", c.SenderDomain, "err", err)
		} else {
			d.rollup.OnJMRPComplaintThreshold(ctx, tenantID, c.SenderDomain, c.SourceIP, count, today())
		}
	}

	if mErr := moveTo(dir, subProcessed, name); mErr != nil {
		d.log.Warn("move processed file", "file", name, "err", mErr)
	}
}

// resolveJMRPTenant attributes a complaint to a tenant: first by sending domain (like fbl),
// then by the sending IP's owning tenant.
func (d *daemon) resolveJMRPTenant(ctx context.Context, c snds.JMRPComplaint, ipTenants map[string]string) (string, bool) {
	if c.SenderDomain != "" {
		if tenant, ok, err := d.pg.ResolveTenantByDomain(ctx, c.SenderDomain); err != nil {
			d.log.Error("resolve tenant by domain", "domain", c.SenderDomain, "err", err)
		} else if ok {
			return tenant, true
		}
	}
	if c.SourceIP != "" {
		if tenant, ok := ipTenants[c.SourceIP]; ok {
			return tenant, true
		}
	}
	return "", false
}

func moveTo(dir, sub, name string) error {
	dstDir := filepath.Join(dir, sub)
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}
	return os.Rename(filepath.Join(dir, name), filepath.Join(dstDir, name))
}

// isComplaintFile mirrors cmd/fbld: accept raw RFC 822 messages, skip archive subdirs, dotfiles
// and files with unrecognized extensions.
func isComplaintFile(name string) bool {
	if strings.HasPrefix(name, ".") {
		return false
	}
	n := strings.ToLower(name)
	switch {
	case strings.HasSuffix(n, ".eml"), strings.HasSuffix(n, ".msg"), strings.HasSuffix(n, ".arf"), strings.HasSuffix(n, ".txt"):
		return true
	case strings.ContainsRune(n, '.'):
		return false
	default:
		return true
	}
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func today() string { return time.Now().UTC().Format("2006-01-02") }

func todayDate() time.Time {
	y, m, d := time.Now().UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
