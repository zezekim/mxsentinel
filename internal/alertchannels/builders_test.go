package alertchannels

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func mustDecode(t *testing.T, raw string) map[string]any {
	t.Helper()
	m, err := DecodeConfig([]byte(raw))
	if err != nil {
		t.Fatalf("decode config: %v", err)
	}
	return m
}

var sampleNote = Notification{
	AlertRef:   "inc-123",
	Title:      "Blacklist hit on 203.0.113.4",
	Kind:       "blacklist",
	Severity:   "critical",
	Domain:     "example.com",
	Summary:    "A blacklist incident is firing",
	LinkURL:    "https://sentinel.example.com/incidents",
	OccurredAt: time.Unix(1700000000, 0).UTC(),
}

// ---- Slack ----------------------------------------------------------------

func TestBuildSlackRequest(t *testing.T) {
	tests := []struct {
		name    string
		cfg     string
		wantErr bool
	}{
		{"ok", `{"webhook_url":"https://hooks.slack.com/services/x"}`, false},
		{"missing url", `{}`, true},
		{"empty url", `{"webhook_url":""}`, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, err := buildSlackRequest(sampleNote, mustDecode(t, tc.cfg))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if req.Method != "POST" {
				t.Errorf("method = %q, want POST", req.Method)
			}
			if req.URL != "https://hooks.slack.com/services/x" {
				t.Errorf("url = %q", req.URL)
			}
			var payload map[string]any
			if err := json.Unmarshal(req.Body, &payload); err != nil {
				t.Fatalf("body not valid json: %v", err)
			}
			if _, ok := payload["attachments"]; !ok {
				t.Errorf("expected attachments in payload")
			}
			if !strings.Contains(string(req.Body), "Blacklist hit") {
				t.Errorf("title missing from body: %s", req.Body)
			}
		})
	}
}

func TestBuildSlackRequestTestPrefix(t *testing.T) {
	n := sampleNote
	n.Test = true
	req, err := buildSlackRequest(n, mustDecode(t, `{"webhook_url":"https://x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(req.Body), "[TEST]") {
		t.Errorf("expected [TEST] prefix, got %s", req.Body)
	}
}

// ---- Webhook --------------------------------------------------------------

func TestBuildWebhookRequest(t *testing.T) {
	req, err := buildWebhookRequest(sampleNote, mustDecode(t, `{"url":"https://example.com/hook"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.URL != "https://example.com/hook" {
		t.Errorf("url = %q", req.URL)
	}
	if _, signed := req.Header[DefaultWebhookSigHeader]; signed {
		t.Errorf("did not expect a signature without a signing secret")
	}
	var payload map[string]any
	if err := json.Unmarshal(req.Body, &payload); err != nil {
		t.Fatalf("body not json: %v", err)
	}
	if payload["alert_ref"] != "inc-123" {
		t.Errorf("alert_ref = %v", payload["alert_ref"])
	}
}

func TestBuildWebhookRequestMissingURL(t *testing.T) {
	if _, err := buildWebhookRequest(sampleNote, mustDecode(t, `{}`)); err == nil {
		t.Fatalf("expected error for missing url")
	}
}

func TestBuildWebhookRequestHMAC(t *testing.T) {
	cfg := mustDecode(t, `{"url":"https://x","signing_secret":"s3cr3t"}`)
	req, err := buildWebhookRequest(sampleNote, cfg)
	if err != nil {
		t.Fatal(err)
	}
	got := req.Header[DefaultWebhookSigHeader]
	if got == "" {
		t.Fatalf("expected signature header %s to be set", DefaultWebhookSigHeader)
	}
	want := signBody("s3cr3t", req.Body)
	if got != want {
		t.Errorf("signature = %q, want %q", got, want)
	}
	if !strings.HasPrefix(got, "sha256=") {
		t.Errorf("signature missing sha256= prefix: %q", got)
	}
}

func TestBuildWebhookRequestCustomSigHeader(t *testing.T) {
	cfg := mustDecode(t, `{"url":"https://x","signing_secret":"k","signature_header":"X-Sig"}`)
	req, err := buildWebhookRequest(sampleNote, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if req.Header["X-Sig"] == "" {
		t.Errorf("expected custom X-Sig header to be set")
	}
	if _, ok := req.Header[DefaultWebhookSigHeader]; ok {
		t.Errorf("did not expect default header when custom one is set")
	}
}

func TestSignBodyDeterministic(t *testing.T) {
	a := signBody("secret", []byte("hello"))
	b := signBody("secret", []byte("hello"))
	if a != b {
		t.Errorf("signBody not deterministic: %q vs %q", a, b)
	}
	if signBody("secret", []byte("hello")) == signBody("other", []byte("hello")) {
		t.Errorf("different secrets produced same signature")
	}
}

// ---- PagerDuty ------------------------------------------------------------

func TestBuildPagerDutyRequest(t *testing.T) {
	req, err := buildPagerDutyRequest(sampleNote, mustDecode(t, `{"routing_key":"R123"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.URL != pagerDutyEventsURL {
		t.Errorf("url = %q", req.URL)
	}
	var payload map[string]any
	if err := json.Unmarshal(req.Body, &payload); err != nil {
		t.Fatalf("body not json: %v", err)
	}
	if payload["routing_key"] != "R123" {
		t.Errorf("routing_key = %v", payload["routing_key"])
	}
	if payload["event_action"] != "trigger" {
		t.Errorf("event_action = %v", payload["event_action"])
	}
	if payload["dedup_key"] != "inc-123" {
		t.Errorf("dedup_key = %v, want inc-123", payload["dedup_key"])
	}
	p, _ := payload["payload"].(map[string]any)
	if p["severity"] != "critical" {
		t.Errorf("severity = %v, want critical", p["severity"])
	}
}

func TestBuildPagerDutyRequestMissingKey(t *testing.T) {
	if _, err := buildPagerDutyRequest(sampleNote, mustDecode(t, `{}`)); err == nil {
		t.Fatalf("expected error for missing routing_key")
	}
}

func TestPagerDutySeverityMapping(t *testing.T) {
	cases := map[string]string{"critical": "critical", "warning": "warning", "info": "info", "": "warning"}
	for in, want := range cases {
		if got := pagerDutySeverity(in); got != want {
			t.Errorf("pagerDutySeverity(%q) = %q, want %q", in, got, want)
		}
	}
}

// ---- Email ----------------------------------------------------------------

func TestBuildEmailMessage(t *testing.T) {
	cfg := mustDecode(t, `{"to":["ops@example.com","oncall@example.com"]}`)
	msg, err := buildEmailMessage(sampleNote, cfg, "alerts@mxsentinel.io")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msg.To) != 2 {
		t.Errorf("expected 2 recipients, got %d", len(msg.To))
	}
	if msg.From != "alerts@mxsentinel.io" {
		t.Errorf("from = %q", msg.From)
	}
	if !strings.HasPrefix(msg.Subject, "[MX Sentinel] ") {
		t.Errorf("subject = %q", msg.Subject)
	}
	if !strings.Contains(msg.Body, "example.com") {
		t.Errorf("body missing domain: %q", msg.Body)
	}
}

func TestBuildEmailMessageConfigFromOverride(t *testing.T) {
	cfg := mustDecode(t, `{"to":["a@b.com"],"from":"custom@x.io"}`)
	msg, err := buildEmailMessage(sampleNote, cfg, "default@x.io")
	if err != nil {
		t.Fatal(err)
	}
	if msg.From != "custom@x.io" {
		t.Errorf("from = %q, want custom@x.io", msg.From)
	}
}

func TestBuildEmailMessageErrors(t *testing.T) {
	if _, err := buildEmailMessage(sampleNote, mustDecode(t, `{}`), "d@x.io"); err == nil {
		t.Errorf("expected error for no recipients")
	}
	if _, err := buildEmailMessage(sampleNote, mustDecode(t, `{"to":["a@b.com"]}`), ""); err == nil {
		t.Errorf("expected error for no from address")
	}
}

func TestBuildEmailMessageTestPrefix(t *testing.T) {
	n := sampleNote
	n.Test = true
	msg, err := buildEmailMessage(n, mustDecode(t, `{"to":["a@b.com"]}`), "d@x.io")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg.Subject, "[TEST]") {
		t.Errorf("expected [TEST] in subject: %q", msg.Subject)
	}
}

func TestRenderRFC822(t *testing.T) {
	raw := renderRFC822(&EmailMessage{
		From: "a@x.io", To: []string{"b@x.io", "c@x.io"}, Subject: "Hi", Body: "line1\nline2",
	})
	s := string(raw)
	if !strings.Contains(s, "To: b@x.io, c@x.io\r\n") {
		t.Errorf("To header wrong: %q", s)
	}
	if !strings.Contains(s, "Subject: Hi\r\n") {
		t.Errorf("Subject header wrong")
	}
	if !strings.Contains(s, "line1\r\nline2") {
		t.Errorf("body CRLF conversion wrong: %q", s)
	}
}
