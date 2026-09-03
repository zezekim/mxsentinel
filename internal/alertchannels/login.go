package alertchannels

import (
	"fmt"
	"strings"
	"time"
)

// KindLogin is the Notification.Kind used for dashboard sign-in notifications.
const KindLogin = "login"

// Per-channel routing flags. Both are plain (non-secret) config keys, so they round-trip
// through SealConfig/RedactConfig untouched and are visible in the API response.
const (
	// loginAlertsField opts a channel into viewer login notifications. Default off.
	loginAlertsField = "login_alerts"
	// incidentAlertsField opts a channel OUT of the firing-incident feed when set to
	// false. Default on, so every channel that predates this flag keeps behaving as it
	// did — and a Telegram bot meant only for login alerts can be made login-only.
	incidentAlertsField = "incident_alerts"
)

// LoginAlertsEnabled reports whether a channel's (decoded) config opts it into login
// notifications. Channels default to off: an operator who wired Slack up for incidents
// does not suddenly get a message on every sign-in.
func LoginAlertsEnabled(cfg map[string]any) bool { return cfgBool(cfg, loginAlertsField) }

// IncidentAlertsEnabled reports whether a channel should receive the firing-incident feed
// that notifyd fans out. Absent flag = true: a channel is an incident destination unless
// the operator says otherwise, which is what every channel created before this flag
// existed expects.
func IncidentAlertsEnabled(cfg map[string]any) bool {
	return cfgBoolDefault(cfg, incidentAlertsField, true)
}

// LoginEvent describes one successful dashboard sign-in. It carries only account and
// request metadata — never a password, session token, or anything from the mail stream.
// Which sign-ins are reported at all is the caller's decision (apid notifies viewer-account
// logins only; see internal/api/login_alerts.go).
type LoginEvent struct {
	UserID    string
	Email     string
	Role      string
	IP        string
	Country   string // ISO-3166-1 alpha-2, when the edge resolved one; may be ""
	UserAgent string
	At        time.Time
}

// LoginNotification renders a sign-in into the tenant-facing Notification shape.
//
// AlertRef is unique per event, so the dispatcher's dedup can never collapse two sign-ins
// into one; SkipSuppression additionally exempts it from the per-channel throttle, since a
// login the operator did not perform is exactly the message that must not be dropped
// because an unrelated incident was flapping.
func LoginNotification(ev LoginEvent, dashboardURL string) Notification {
	at := ev.At
	if at.IsZero() {
		at = time.Now()
	}
	at = at.UTC()

	link := ""
	if dashboardURL != "" {
		link = strings.TrimRight(dashboardURL, "/") + "/account"
	}

	var sum strings.Builder
	fmt.Fprintf(&sum, "%s signed in to the MX Sentinel dashboard", ev.Email)
	if ev.Role != "" {
		fmt.Fprintf(&sum, " as %s", ev.Role)
	}
	sum.WriteString(".")
	if ev.IP != "" {
		fmt.Fprintf(&sum, " IP: %s", ev.IP)
		if ev.Country != "" {
			fmt.Fprintf(&sum, " (%s)", ev.Country)
		}
		sum.WriteString(".")
	}
	if ua := truncate(ev.UserAgent, 120); ua != "" {
		fmt.Fprintf(&sum, " Client: %s", ua)
	}

	title := "Dashboard login: " + ev.Email
	if ev.Role != "" {
		title += " (" + ev.Role + ")"
	}

	return Notification{
		AlertRef:        fmt.Sprintf("login:%s:%d", ev.UserID, at.UnixNano()),
		Title:           title,
		Kind:            KindLogin,
		Severity:        "info",
		Summary:         sum.String(),
		LinkURL:         link,
		OccurredAt:      at,
		SkipSuppression: true,
	}
}

// truncate shortens s to at most n runes, appending an ellipsis when it cuts.
func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
