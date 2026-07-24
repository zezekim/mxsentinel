// Command fbld ingests feedback-loop (ARF) complaint emails and Gmail Postmaster Tools
// reputation, building a per-sending-domain complaint reputation and opening critical
// incidents when a domain crosses the 24h complaint threshold or Postmaster grades it
// LOW/BAD.
//
// It mirrors cmd/dmarcd's drop-directory pattern: it polls a directory (env FBL_DIR,
// default /fbl-drop, overridable with --dir) for ARF complaint emails, parses each
// (RFC 5965 multipart/report; report-type=feedback-report), records it, and archives the
// file to a "processed" (or "quarantine") subdir. Separately, on an interval, it refreshes
// Gmail Postmaster reputation — but ONLY when GOOGLE_POSTMASTER_TOKEN is set; otherwise
// that half is skipped cleanly (logged once).
//
// Attribution is per SENDING DOMAIN, not the SASL login: a shared relay authenticates with
// one credential for hundreds of client domains, so abuse/reputation must be isolated per
// client (same rationale as cmd/abused).
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
	"github.com/zezekim/mxsentinel/internal/fbl"
	"github.com/zezekim/mxsentinel/internal/integrationsettings"
	"github.com/zezekim/mxsentinel/internal/obs"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
)

const (
	subProcessed  = "processed"
	subQuarantine = "quarantine"
)

func main() {
	var (
		dir      = flag.String("dir", "", "drop directory to watch for ARF complaint files (default $FBL_DIR or /fbl-drop)")
		interval = flag.Duration("interval", 30*time.Second, "drop-directory scan interval")
		pmEvery  = flag.Duration("postmaster-interval", 6*time.Hour, "how often to refresh Gmail Postmaster reputation")
	)
	flag.Parse()

	if err := run(*dir, *interval, *pmEvery); err != nil {
		fmt.Fprintln(os.Stderr, "fbld:", err)
		os.Exit(1)
	}
}

func run(dir string, interval, pmEvery time.Duration) error {
	if dir == "" {
		dir = os.Getenv("FBL_DIR")
	}
	if dir == "" {
		dir = "/fbl-drop"
	}

	cfg, err := config.Load(os.Getenv("MXS_CONFIG"))
	if err != nil {
		return err
	}
	log := obs.NewLogger("fbld", cfg.LogLevel)

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

	store := fbl.NewStore(pg.Pool)
	rollup := fbl.NewRollup(pg, log, complaintThreshold())

	// Resolve the Gmail Postmaster token: dashboard (Settings → Providers & keys) wins over
	// GOOGLE_POSTMASTER_TOKEN. Applied at startup (restart to pick up a change).
	pmToken := os.Getenv("GOOGLE_POSTMASTER_TOKEN")
	if tenantID := strings.TrimSpace(os.Getenv("RELAY_TENANT_ID")); tenantID != "" {
		enc, _, encErr := crypto.NewEncryptor(cfg.Integration.EncryptionKey)
		if encErr != nil {
			log.Warn("init encryptor for provider settings", "err", encErr)
		}
		rs := integrationsettings.Resolve(ctx, pg, enc, tenantID, log)
		pmToken = integrationsettings.Prefer(rs.PostmasterToken, pmToken)
	}

	d := &daemon{
		log:    log,
		store:  store,
		rollup: rollup,
		pm:     newPostmaster(log, pmToken),
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Warn("create drop dir", "dir", dir, "err", err)
	}

	srv.SetReady(true)
	log.Info("fbld started",
		"dir", dir, "interval", interval.String(),
		"complaint_threshold", rollup.Threshold(),
		"postmaster_enabled", d.pm != nil, "postmaster_interval", pmEvery.String())

	// Run an initial scan and (if enabled) Postmaster refresh on startup, then on ticks.
	d.scan(ctx, dir)
	d.refreshPostmaster(ctx)

	scanTick := time.NewTicker(interval)
	defer scanTick.Stop()
	pmTick := time.NewTicker(pmEvery)
	defer pmTick.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info("fbld shutting down")
			return nil
		case <-scanTick.C:
			d.scan(ctx, dir)
		case <-pmTick.C:
			d.refreshPostmaster(ctx)
		}
	}
}

// complaintThreshold reads FBL_COMPLAINT_THRESHOLD (default 10).
func complaintThreshold() int {
	v := strings.TrimSpace(os.Getenv("FBL_COMPLAINT_THRESHOLD"))
	if v == "" {
		return 10
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil || n <= 0 {
		return 10
	}
	return n
}

// newPostmaster builds the Postmaster client from GOOGLE_POSTMASTER_TOKEN, or returns nil
// (logging once) when the env is unset so the Postmaster half is skipped cleanly.
func newPostmaster(log *slog.Logger, token string) *fbl.PostmasterClient {
	pm, ok := fbl.NewPostmasterClient(token)
	if !ok {
		log.Info("no Gmail Postmaster token (dashboard or GOOGLE_POSTMASTER_TOKEN); skipping Gmail Postmaster reputation (FBL complaint ingestion still active)")
		return nil
	}
	return pm
}

type daemon struct {
	log    *slog.Logger
	store  *fbl.Store
	rollup *fbl.Rollup
	pm     *fbl.PostmasterClient // nil when Postmaster is disabled
}

func (d *daemon) scan(ctx context.Context, dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		d.log.Error("read drop dir", "dir", dir, "err", err)
		return
	}
	for _, e := range entries {
		if ctx.Err() != nil {
			return
		}
		if e.IsDir() || !isComplaintFile(e.Name()) {
			continue
		}
		d.processFile(ctx, dir, e.Name())
	}
}

func (d *daemon) processFile(ctx context.Context, dir, name string) {
	full := filepath.Join(dir, name)
	data, err := os.ReadFile(full)
	if err != nil {
		d.log.Error("read complaint", "file", name, "err", err)
		return
	}

	pc, err := fbl.ParseARF(data)
	if err != nil {
		d.log.Warn("malformed ARF complaint; quarantining", "file", name, "err", err)
		if mErr := moveTo(dir, subQuarantine, name); mErr != nil {
			d.log.Warn("move quarantined file", "file", name, "err", mErr)
		}
		return
	}

	if err := d.store.InsertComplaint(ctx, fbl.Complaint(pc)); err != nil {
		// Infrastructure error (DB) — leave the file in place to retry.
		d.log.Error("record complaint failed; will retry", "file", name, "err", err)
		return
	}

	d.log.Info("processed FBL complaint",
		"file", name, "feedback_type", pc.FeedbackType,
		"sender_domain", pc.SenderDomain, "provider", pc.Provider)

	// Rollup: trip a critical incident if this domain crossed the 24h threshold.
	if pc.SenderDomain != "" {
		if count, err := d.store.ComplaintCount24h(ctx, pc.SenderDomain); err != nil {
			d.log.Error("complaint count 24h", "domain", pc.SenderDomain, "err", err)
		} else {
			d.rollup.OnComplaintThreshold(ctx, pc.SenderDomain, count, today())
		}
	}

	if mErr := moveTo(dir, subProcessed, name); mErr != nil {
		d.log.Warn("move processed file", "file", name, "err", mErr)
	}
}

// refreshPostmaster pulls reputation for every verified Postmaster domain, stores it, and
// trips an incident on a LOW/BAD grade. No-op when Postmaster is disabled.
func (d *daemon) refreshPostmaster(ctx context.Context) {
	if d.pm == nil {
		return
	}
	domains, err := d.pm.ListDomains(ctx)
	if err != nil {
		d.log.Warn("postmaster list domains failed; skipping this cycle", "err", err)
		return
	}
	day := today()
	for _, domain := range domains {
		if ctx.Err() != nil {
			return
		}
		rep, ok, err := d.pm.FetchDomain(ctx, domain)
		if err != nil {
			d.log.Warn("postmaster fetch domain failed", "domain", domain, "err", err)
			continue
		}
		if !ok {
			continue // no stats yet for this domain
		}
		if err := d.store.UpsertReputation(ctx, domain, "postmaster", rep.Reputation, rep.SpamRate); err != nil {
			d.log.Error("store reputation", "domain", domain, "err", err)
			continue
		}
		d.log.Info("postmaster reputation refreshed",
			"domain", domain, "reputation", rep.Reputation, "spam_rate", rep.SpamRate)
		d.rollup.OnPostmasterReputation(ctx, rep, day)
	}
}

func moveTo(dir, sub, name string) error {
	dstDir := filepath.Join(dir, sub)
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}
	return os.Rename(filepath.Join(dir, name), filepath.Join(dstDir, name))
}

// isComplaintFile keeps the watcher from picking up our own archive subdirs or dotfiles.
// ARF complaints arrive as raw RFC 822 messages; common drop names are *.eml / *.msg / no
// extension. We accept anything that isn't an obvious non-message.
func isComplaintFile(name string) bool {
	if strings.HasPrefix(name, ".") {
		return false
	}
	n := strings.ToLower(name)
	switch {
	case strings.HasSuffix(n, ".eml"), strings.HasSuffix(n, ".msg"), strings.HasSuffix(n, ".arf"), strings.HasSuffix(n, ".txt"):
		return true
	case strings.ContainsRune(n, '.'):
		// Has an extension we don't recognize (e.g. .xml, .gz) — skip to avoid clashing
		// with other drop-dir consumers if a directory is ever shared.
		return false
	default:
		return true // extensionless maildir-style message
	}
}

func today() string { return time.Now().UTC().Format("2006-01-02") }
