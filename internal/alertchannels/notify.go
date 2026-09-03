// Package alertchannels implements outbound alert delivery for MX Sentinel. A firing
// alert or incident is fanned out to a tenant's enabled notification channels — Slack
// (incoming webhook), a generic webhook (POST JSON with an optional HMAC signature),
// PagerDuty (Events API v2), email, and Telegram (Bot API sendMessage).
//
// Design:
//   - Each channel driver (Notifier) turns a Notification + channel config into a request
//     payload with a PURE, table-driven-tested function (buildSlackRequest,
//     buildWebhookRequest, buildPagerDutyRequest, buildEmailMessage). No I/O happens in
//     those functions, so they are trivially unit-testable.
//   - The actual network send is hidden behind the HTTPDoer / Mailer interfaces. Tests
//     inject fakes; nothing in `make test` touches the network or sends a real
//     notification.
//   - The Dispatcher fans a Notification out to a set of channels and enforces dedup
//     (never notify the same channel twice for one alert) and per-channel throttling (a
//     flapping alert cannot spam a channel). Its decisions are driven by an injected clock
//     and a DeliveryStore, so they are deterministic under test.
//
// Privacy: a Notification carries only incident metadata (title, kind, severity, domain).
// Message bodies and subject lines never reach this package — the boundary is enforced at
// the telemetry parser upstream.
package alertchannels

import (
	"context"
	"time"
)

// Channel types.
const (
	TypeSlack     = "slack"
	TypeWebhook   = "webhook"
	TypePagerDuty = "pagerduty"
	TypeEmail     = "email"
	TypeTelegram  = "telegram"
)

// ValidTypes is the set of accepted channel types.
var ValidTypes = map[string]bool{
	TypeSlack:     true,
	TypeWebhook:   true,
	TypePagerDuty: true,
	TypeEmail:     true,
	TypeTelegram:  true,
}

// Notification is the tenant-facing, body-free description of a firing alert/incident that
// gets rendered into each channel's payload. AlertRef is the dedup key (an incident id, or
// "test:<uuid>" for the manual test action).
type Notification struct {
	AlertRef   string    // stable id for dedup (incident id, or "test:<uuid>")
	Title      string    // human title, e.g. "Blacklist hit on 203.0.113.4"
	Kind       string    // incident kind: rejection_spike | blacklist | dns_validation | other
	Severity   string    // info | warning | critical
	Domain     string    // affected domain, may be ""
	Summary    string    // short body text (metadata only, never message content)
	LinkURL    string    // deep link back into the dashboard, may be ""
	OccurredAt time.Time // when the underlying signal fired
	Test       bool      // true for the "send test notification" action
	// SkipSuppression delivers the notification even if the channel is inside its throttle
	// window. Set for events that are individually meaningful and must never be swallowed
	// by a flapping incident — a dashboard login is one (see LoginNotification).
	SkipSuppression bool
}

// HTTPRequest is a fully-rendered outbound HTTP request produced by a driver's pure build
// function. The HTTPDoer performs the send.
type HTTPRequest struct {
	Method string
	URL    string
	Header map[string]string
	Body   []byte
}

// EmailMessage is a fully-rendered email produced by buildEmailMessage. The Mailer performs
// the send.
type EmailMessage struct {
	From    string
	To      []string
	Subject string
	Body    string // plain text; metadata only
}

// HTTPDoer sends an HTTPRequest. A non-2xx response must be reported as an error so the
// dispatcher records the delivery as failed. Implemented for real by httpDoer; faked in
// tests.
type HTTPDoer interface {
	Do(ctx context.Context, req *HTTPRequest) error
}

// Mailer sends an EmailMessage. Implemented for real by smtpMailer; faked in tests.
type Mailer interface {
	Send(ctx context.Context, msg *EmailMessage) error
}

// Notifier is a delivery driver for one channel type. Send validates config, builds the
// payload with the driver's pure function, and dispatches it through the injected transport.
type Notifier interface {
	// Type returns the channel type this driver handles.
	Type() string
	// Send delivers n to the destination described by cfg (already decrypted).
	Send(ctx context.Context, n Notification, cfg map[string]any) error
}

// severityRank maps a severity label to a comparable rank (higher = worse). Unknown labels
// rank as warning.
func severityRank(sev string) int {
	switch sev {
	case "critical":
		return 3
	case "warning":
		return 2
	case "info":
		return 1
	default:
		return 2
	}
}

// cfgString safely reads a string field from a decoded config map.
func cfgString(cfg map[string]any, key string) string {
	if cfg == nil {
		return ""
	}
	if v, ok := cfg[key].(string); ok {
		return v
	}
	return ""
}

// cfgBool safely reads a boolean field from a decoded config map. JSON booleans and the
// strings "true"/"1" both count as true, since config can be hand-edited or posted by a
// form that stringifies its values.
func cfgBool(cfg map[string]any, key string) bool {
	if cfg == nil {
		return false
	}
	switch v := cfg[key].(type) {
	case bool:
		return v
	case string:
		return v == "true" || v == "1"
	}
	return false
}

// cfgBoolDefault is cfgBool with an explicit default for an absent key, so a flag can
// default to true without every existing channel config having to spell it out.
func cfgBoolDefault(cfg map[string]any, key string, def bool) bool {
	if cfg == nil {
		return def
	}
	if _, present := cfg[key]; !present {
		return def
	}
	return cfgBool(cfg, key)
}

// cfgStringSlice reads a []string field from a decoded config map. Accepts both a JSON
// array of strings and a single string.
func cfgStringSlice(cfg map[string]any, key string) []string {
	if cfg == nil {
		return nil
	}
	switch v := cfg[key].(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		if v != "" {
			return []string{v}
		}
	}
	return nil
}
