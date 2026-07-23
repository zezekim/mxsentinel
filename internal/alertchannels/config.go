package alertchannels

import (
	"os"
	"strconv"
	"time"
)

// Config controls the notifyd daemon: how often it scans for firing incidents, the dedup
// and throttle windows applied per channel, the SMTP relay used for the email driver, and
// the dashboard base URL used to build deep links in notifications.
//
// Precedent: internal/rbl.LoadConfig — a feature-local config read from MXS_* env vars. The
// keys are also listed in INTEGRATION_alert-channels.md for wiring into the central config.
type Config struct {
	PollInterval time.Duration // how often notifyd scans for new firing incidents
	Lookback     time.Duration // how far back each scan looks (must exceed PollInterval)
	Throttle     time.Duration // min gap between two sends to the same channel
	Dedup        time.Duration // window suppressing a repeated alertRef to a channel
	HTTPTimeout  time.Duration // per-request timeout for webhook/slack/pagerduty sends
	DashboardURL string        // base URL for deep links, e.g. https://sentinel.example.com
	SMTPAddr     string        // host:port for the email driver
	SMTPUsername string
	SMTPPassword string
	SMTPFrom     string // default From address for email notifications
}

// LoadConfig reads notifyd configuration from MXS_NOTIFY_* (and MXS_SMTP_*) env vars,
// falling back to safe defaults.
func LoadConfig() Config {
	c := Config{
		PollInterval: 30 * time.Second,
		Lookback:     10 * time.Minute,
		Throttle:     15 * time.Minute,
		Dedup:        6 * time.Hour,
		HTTPTimeout:  10 * time.Second,
		DashboardURL: envStr("MXS_NOTIFY_DASHBOARD_URL", ""),
		SMTPAddr:     envStr("MXS_SMTP_ADDR", ""),
		SMTPUsername: envStr("MXS_SMTP_USERNAME", ""),
		SMTPPassword: envStr("MXS_SMTP_PASSWORD", ""),
		SMTPFrom:     envStr("MXS_SMTP_FROM", ""),
	}
	c.PollInterval = envDur("MXS_NOTIFY_POLL_INTERVAL", c.PollInterval)
	c.Lookback = envDur("MXS_NOTIFY_LOOKBACK", c.Lookback)
	c.Throttle = envDur("MXS_NOTIFY_THROTTLE", c.Throttle)
	c.Dedup = envDur("MXS_NOTIFY_DEDUP", c.Dedup)
	c.HTTPTimeout = envDur("MXS_NOTIFY_HTTP_TIMEOUT", c.HTTPTimeout)
	if c.Lookback < c.PollInterval {
		c.Lookback = 2 * c.PollInterval
	}
	return c
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envDur accepts either a Go duration string ("30s", "5m") or a bare integer (seconds).
func envDur(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	if d, err := time.ParseDuration(v); err == nil {
		return d
	}
	if n, err := strconv.Atoi(v); err == nil {
		return time.Duration(n) * time.Second
	}
	return def
}
