// Command notifyd is the alert-delivery daemon. It periodically scans for firing incidents
// and fans each one out to the affected tenant's enabled alert channels (Slack, generic
// webhook, PagerDuty, email). Per-channel dedup and throttling (backed by the
// alert_deliveries log) stop a flapping alert from spamming a channel.
//
// notifyd only ever handles incident metadata (title, kind, severity, domain) — never
// message bodies or subject lines. See docs/alert-channels.md.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/zezekim/mxsentinel/internal/alertchannels"
	"github.com/zezekim/mxsentinel/internal/config"
	"github.com/zezekim/mxsentinel/internal/crypto"
	"github.com/zezekim/mxsentinel/internal/obs"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "notifyd:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Getenv("MXS_CONFIG"))
	if err != nil {
		return err
	}
	log := obs.NewLogger("notifyd", cfg.LogLevel)
	nc := alertchannels.LoadConfig()

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

	enc, encrypted, err := crypto.NewEncryptor(cfg.Integration.EncryptionKey)
	if err != nil {
		return err
	}
	if !encrypted {
		log.Warn("no encryption key set; channel secrets are read as plaintext")
	}

	// Overlay the dashboard-managed notifyd tuning (Settings → Delivery & data tuning) over env;
	// dashboard values win when set. Applied at startup (restart to pick up a change). A DB read
	// error is logged and treated as "not set" so a transient issue never blanks the env config.
	if tenantID := strings.TrimSpace(os.Getenv("RELAY_TENANT_ID")); tenantID != "" {
		if t, terr := pg.GetDeliveryTuning(ctx, tenantID); terr != nil {
			log.Warn("read dashboard delivery tuning; using env config", "err", terr)
		} else {
			nt := t.Notify
			if nt.PollIntervalSecs > 0 {
				nc.PollInterval = time.Duration(nt.PollIntervalSecs) * time.Second
			}
			if nt.ThrottleSecs > 0 {
				nc.Throttle = time.Duration(nt.ThrottleSecs) * time.Second
			}
			if nt.DedupSecs > 0 {
				nc.Dedup = time.Duration(nt.DedupSecs) * time.Second
			}
			if nt.LookbackSecs > 0 {
				nc.Lookback = time.Duration(nt.LookbackSecs) * time.Second
			}
			if nt.HTTPTimeoutSecs > 0 {
				nc.HTTPTimeout = time.Duration(nt.HTTPTimeoutSecs) * time.Second
			}
			if nt.DashboardURL != "" {
				nc.DashboardURL = nt.DashboardURL
			}
			if nc.Lookback < nc.PollInterval { // preserve LoadConfig's invariant after overlay
				nc.Lookback = 2 * nc.PollInterval
			}
		}
	}

	w := &worker{
		log: log,
		pg:  pg,
		enc: enc,
		dispatcher: &alertchannels.Dispatcher{
			Notifiers: alertchannels.NewRegistry(nc),
			Store:     pg,
			Throttle:  nc.Throttle,
			Dedup:     nc.Dedup,
			Log:       log,
		},
		lookback:     nc.Lookback,
		dashboardURL: nc.DashboardURL,
		// Start the watermark one lookback in the past so a just-started daemon still
		// notifies on incidents that fired moments before boot.
		watermark: time.Now().Add(-nc.Lookback),
	}

	srv.SetReady(true)
	log.Info("notifyd started",
		"poll_interval", nc.PollInterval.String(),
		"throttle", nc.Throttle.String(),
		"dedup", nc.Dedup.String())

	ticker := time.NewTicker(nc.PollInterval)
	defer ticker.Stop()
	w.scan(ctx)
	for {
		select {
		case <-ctx.Done():
			log.Info("notifyd shutting down")
			return nil
		case <-ticker.C:
			w.scan(ctx)
		}
	}
}

type worker struct {
	log          *slog.Logger
	pg           *pgstore.Store
	enc          *crypto.Encryptor
	dispatcher   *alertchannels.Dispatcher
	lookback     time.Duration
	dashboardURL string
	watermark    time.Time
}

// scan reads firing incidents since the watermark and dispatches each to its tenant's
// enabled channels. Channels are cached per tenant within a single scan.
func (w *worker) scan(ctx context.Context) {
	since := w.watermark.Add(-w.lookback) // overlap so nothing is missed; dedup handles repeats
	incidents, err := w.pg.ListFiringIncidentsSince(ctx, since, 500)
	if err != nil {
		w.log.Error("list firing incidents", "err", err)
		return
	}

	channelCache := map[string][]alertchannels.Channel{}
	for _, inc := range incidents {
		if ctx.Err() != nil {
			return
		}
		channels, ok := channelCache[inc.TenantID]
		if !ok {
			channels = w.loadChannels(ctx, inc.TenantID)
			channelCache[inc.TenantID] = channels
		}
		if len(channels) == 0 {
			continue
		}
		note := w.toNotification(inc)
		results := w.dispatcher.Dispatch(ctx, channels, note)
		for _, res := range results {
			if res.Status == alertchannels.StatusSent {
				w.log.Info("alert delivered",
					"tenant_id", inc.TenantID, "channel_id", res.ChannelID,
					"type", res.Type, "alert_ref", note.AlertRef)
			} else if res.Status == alertchannels.StatusFailed {
				w.log.Warn("alert delivery failed",
					"tenant_id", inc.TenantID, "channel_id", res.ChannelID,
					"type", res.Type, "alert_ref", note.AlertRef, "err", res.Err)
			}
		}
	}

	w.watermark = time.Now()
}

// loadChannels fetches a tenant's enabled channels and decrypts their configs for dispatch.
func (w *worker) loadChannels(ctx context.Context, tenantID string) []alertchannels.Channel {
	rows, err := w.pg.ListEnabledAlertChannels(ctx, tenantID)
	if err != nil {
		w.log.Error("list enabled channels", "tenant_id", tenantID, "err", err)
		return nil
	}
	out := make([]alertchannels.Channel, 0, len(rows))
	for _, c := range rows {
		plain, err := alertchannels.OpenConfig(w.enc, c.Type, c.Config)
		if err != nil {
			w.log.Warn("decrypt channel config", "tenant_id", tenantID, "channel_id", c.ID, "err", err)
			continue
		}
		out = append(out, alertchannels.Channel{
			ID:       c.ID,
			TenantID: c.TenantID,
			Type:     c.Type,
			Name:     c.Name,
			Config:   plain,
			Enabled:  c.Enabled,
		})
	}
	return out
}

func (w *worker) toNotification(inc pgstore.FiringIncident) alertchannels.Notification {
	link := ""
	if w.dashboardURL != "" {
		link = w.dashboardURL + "/incidents"
	}
	return alertchannels.Notification{
		AlertRef:   inc.ID,
		Title:      inc.Title,
		Kind:       inc.Kind,
		Severity:   inc.Severity,
		Domain:     inc.Domain,
		Summary:    fmt.Sprintf("A %s incident is firing", inc.Kind),
		LinkURL:    link,
		OccurredAt: inc.CreatedAt,
	}
}
