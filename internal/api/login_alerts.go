package api

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/zezekim/mxsentinel/internal/alertchannels"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
)

// Login notifications: when the tenant's viewer account signs in to the dashboard, every
// alert channel whose config carries "login_alerts": true gets a message. This is the
// security-notification path (e.g. a Telegram bot that pings the operator's phone when the
// customer-facing viewer login is used), so it is deliberately opt-in per channel — a Slack
// channel wired up for incidents does not start reporting sign-ins on its own.
//
// Only the viewer role is reported. Owner/admin/operator sign-ins are the operator's own
// day-to-day traffic; alerting on those would bury the one sign-in that is worth seeing.
//
// Delivery is best-effort and asynchronous, exactly like the audit log: a notification
// failure must never turn a valid login into an error. Only account/request metadata
// travels (email, role, source IP + country, user agent) — never the password or the
// session token.
// See docs/alert-channels.md.

// loginAlertRole is the only role whose sign-ins are notified (see the package comment
// above). Roles come from users.role: owner | admin | operator | viewer.
const loginAlertRole = "viewer"

// loginAlertable reports whether a sign-in by this role is worth a notification.
func loginAlertable(role string) bool { return role == loginAlertRole }

// loginNotifyTimeout bounds the whole fan-out for one login, including the outbound sends.
const loginNotifyTimeout = 15 * time.Second

// loginDispatcher lazily builds the dispatcher used for login notifications. Building it
// per login would be cheap (logins are rare) but this keeps one HTTP client for the
// process, matching notifyd.
type loginDispatcher struct {
	once sync.Once
	cfg  alertchannels.Config
	disp *alertchannels.Dispatcher
}

func (l *loginDispatcher) get(s *Server) (*alertchannels.Dispatcher, alertchannels.Config) {
	l.once.Do(func() {
		l.cfg = alertchannels.LoadConfig()
		l.disp = &alertchannels.Dispatcher{
			Notifiers: alertchannels.NewRegistry(l.cfg),
			Store:     s.pg,
			// Dedup/throttle are left off: LoginNotification already carries a unique
			// AlertRef and SkipSuppression, and a swallowed sign-in alert is worse than
			// a duplicate one.
			Log: s.log,
		}
	})
	return l.disp, l.cfg
}

// notifyLogin fans a successful sign-in out to the tenant's login-alert channels. It
// returns immediately; the work happens on its own goroutine with its own context, so a
// slow Telegram/Slack endpoint cannot delay the login response.
func (s *Server) notifyLogin(r *http.Request, u pgstore.User) {
	if !loginAlertable(u.Role) {
		return
	}
	ev := alertchannels.LoginEvent{
		UserID:    u.ID,
		Email:     u.Email,
		Role:      u.Role,
		IP:        clientIP(r),
		Country:   clientCountry(r),
		UserAgent: r.UserAgent(),
		At:        time.Now().UTC(),
	}
	tenantID := u.TenantID

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), loginNotifyTimeout)
		defer cancel()

		channels := s.loginAlertChannels(ctx, tenantID)
		if len(channels) == 0 {
			return
		}

		disp, cfg := s.loginAlerts.get(s)
		dashboard := s.publicBaseURL
		if dashboard == "" {
			dashboard = cfg.DashboardURL
		}

		for _, res := range disp.Dispatch(ctx, channels, alertchannels.LoginNotification(ev, dashboard)) {
			if res.Status == alertchannels.StatusFailed {
				s.log.Warn("login notification failed",
					"tenant_id", tenantID, "channel_id", res.ChannelID,
					"type", res.Type, "err", res.Err)
			}
		}
	}()
}

// loginAlertChannels returns the tenant's enabled channels that opted into login alerts,
// with their secrets decrypted for dispatch.
func (s *Server) loginAlertChannels(ctx context.Context, tenantID string) []alertchannels.Channel {
	rows, err := s.pg.ListEnabledAlertChannels(ctx, tenantID)
	if err != nil {
		s.log.Warn("login notification: list channels", "tenant_id", tenantID, "err", err)
		return nil
	}
	out := make([]alertchannels.Channel, 0, len(rows))
	for _, c := range rows {
		plain, err := alertchannels.OpenConfig(s.enc, c.Type, c.Config)
		if err != nil {
			s.log.Warn("login notification: decrypt channel config",
				"tenant_id", tenantID, "channel_id", c.ID, "err", err)
			continue
		}
		cfg, err := alertchannels.DecodeConfig(plain)
		if err != nil || !alertchannels.LoginAlertsEnabled(cfg) {
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
