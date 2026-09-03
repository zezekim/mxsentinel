package alertchannels

import (
	"context"
	"strings"
	"testing"
	"time"
)

var sampleLogin = LoginEvent{
	UserID:    "u-1",
	Email:     "viewer@example.com",
	Role:      "viewer",
	IP:        "203.0.113.9",
	UserAgent: "Mozilla/5.0 (Macintosh)",
	At:        time.Unix(1700000000, 0).UTC(),
}

func TestLoginNotification(t *testing.T) {
	n := LoginNotification(sampleLogin, "https://sentinel.example.com/")

	if n.Kind != KindLogin {
		t.Errorf("kind = %q, want %q", n.Kind, KindLogin)
	}
	if !n.SkipSuppression {
		t.Error("login notifications must bypass the per-channel throttle")
	}
	if n.Test {
		t.Error("a real login is not a test notification")
	}
	for _, want := range []string{"viewer@example.com", "viewer"} {
		if !strings.Contains(n.Title, want) {
			t.Errorf("title %q missing %q", n.Title, want)
		}
	}
	for _, want := range []string{"viewer@example.com", "203.0.113.9", "Mozilla/5.0"} {
		if !strings.Contains(n.Summary, want) {
			t.Errorf("summary %q missing %q", n.Summary, want)
		}
	}
	if n.LinkURL != "https://sentinel.example.com/account" {
		t.Errorf("link = %q", n.LinkURL)
	}
	if !n.OccurredAt.Equal(sampleLogin.At) {
		t.Errorf("occurred_at = %v, want %v", n.OccurredAt, sampleLogin.At)
	}
}

// Two sign-ins by the same user must produce different AlertRefs, or the dispatcher's
// dedup would collapse the second one into silence.
func TestLoginNotificationRefIsUnique(t *testing.T) {
	a := LoginNotification(sampleLogin, "")
	second := sampleLogin
	second.At = sampleLogin.At.Add(time.Nanosecond)
	if b := LoginNotification(second, ""); a.AlertRef == b.AlertRef {
		t.Errorf("two logins share alert_ref %q", a.AlertRef)
	}
	if a.LinkURL != "" {
		t.Errorf("link should be empty when no dashboard URL is configured, got %q", a.LinkURL)
	}
}

func TestLoginNotificationMinimalEvent(t *testing.T) {
	n := LoginNotification(LoginEvent{UserID: "u", Email: "a@b.c"}, "")
	if n.OccurredAt.IsZero() {
		t.Error("occurred_at should default to now")
	}
	if strings.Contains(n.Summary, "as ") || strings.Contains(n.Summary, "IP:") {
		t.Errorf("absent fields should be omitted, got %q", n.Summary)
	}
}

func TestLoginAlertsEnabled(t *testing.T) {
	tests := []struct {
		cfg  string
		want bool
	}{
		{`{"login_alerts":true}`, true},
		{`{"login_alerts":"true"}`, true},
		{`{"login_alerts":false}`, false},
		{`{"login_alerts":"no"}`, false},
		{`{}`, false},
	}
	for _, tc := range tests {
		if got := LoginAlertsEnabled(mustDecode(t, tc.cfg)); got != tc.want {
			t.Errorf("LoginAlertsEnabled(%s) = %v, want %v", tc.cfg, got, tc.want)
		}
	}
	if LoginAlertsEnabled(nil) {
		t.Error("nil config must not opt in")
	}
}

// The opt-in flag is plain config: it must survive sealing and stay visible in redacted
// API output, otherwise a PATCH that round-trips the config would silently disable alerts.
func TestLoginAlertsFlagIsNotSecret(t *testing.T) {
	redacted, err := RedactConfig(TypeTelegram, []byte(`{"bot_token":"s","login_alerts":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(redacted), `"login_alerts":true`) {
		t.Errorf("login_alerts missing from redacted config: %s", redacted)
	}
}

// A login must reach a channel that is inside its throttle window; an unrelated flapping
// incident must never swallow it.
func TestDispatchLoginBypassesThrottle(t *testing.T) {
	store := newFakeStore()
	store.recentSent["ch1"] = true // channel notified moments ago
	d, fn := newDispatcher(store, &fakeNotifier{typ: "webhook"})

	note := LoginNotification(sampleLogin, "")
	res := d.Dispatch(context.Background(), []Channel{testChannel}, note)
	if res[0].Status != StatusSent {
		t.Fatalf("status = %s, want %s", res[0].Status, StatusSent)
	}
	if fn.sent != 1 {
		t.Errorf("notifier.Send called %d times, want 1", fn.sent)
	}
}

// Dedup still applies to a repeated ref — the uniqueness of a login's AlertRef is what
// keeps sign-ins flowing, not an exemption from dedup.
func TestDispatchLoginStillDedups(t *testing.T) {
	store := newFakeStore()
	note := LoginNotification(sampleLogin, "")
	store.delivered["ch1|"+note.AlertRef] = true
	d, fn := newDispatcher(store, &fakeNotifier{typ: "webhook"})

	if res := d.Dispatch(context.Background(), []Channel{testChannel}, note); res[0].Status != StatusSkippedDedup {
		t.Fatalf("status = %s, want %s", res[0].Status, StatusSkippedDedup)
	}
	if fn.sent != 0 {
		t.Errorf("notifier.Send called %d times, want 0", fn.sent)
	}
}

// A channel is an incident destination unless it says otherwise: every channel created
// before the flag existed must keep receiving the incident feed.
func TestIncidentAlertsEnabled(t *testing.T) {
	tests := []struct {
		cfg  string
		want bool
	}{
		{`{}`, true},
		{`{"login_alerts":true}`, true},
		{`{"incident_alerts":true}`, true},
		{`{"incident_alerts":false}`, false},
		{`{"incident_alerts":"false"}`, false},
		{`{"incident_alerts":"true"}`, true},
		{`{"login_alerts":true,"incident_alerts":false}`, false}, // login-only channel
	}
	for _, tc := range tests {
		if got := IncidentAlertsEnabled(mustDecode(t, tc.cfg)); got != tc.want {
			t.Errorf("IncidentAlertsEnabled(%s) = %v, want %v", tc.cfg, got, tc.want)
		}
	}
	if !IncidentAlertsEnabled(nil) {
		t.Error("a nil config must still default to opted in")
	}
}

// The two flags are independent: login-only, incidents-only, both, or neither.
func TestRoutingFlagsAreIndependent(t *testing.T) {
	cfg := mustDecode(t, `{"login_alerts":true,"incident_alerts":false}`)
	if !LoginAlertsEnabled(cfg) || IncidentAlertsEnabled(cfg) {
		t.Errorf("login-only channel misrouted: login=%v incident=%v",
			LoginAlertsEnabled(cfg), IncidentAlertsEnabled(cfg))
	}
	cfg = mustDecode(t, `{}`)
	if LoginAlertsEnabled(cfg) || !IncidentAlertsEnabled(cfg) {
		t.Errorf("default channel misrouted: login=%v incident=%v",
			LoginAlertsEnabled(cfg), IncidentAlertsEnabled(cfg))
	}
}
