package alertchannels

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestBuildTelegramRequest(t *testing.T) {
	tests := []struct {
		name    string
		cfg     string
		wantErr bool
	}{
		{"ok", `{"bot_token":"123:ABC","chat_id":"-1001234567890"}`, false},
		{"missing token", `{"chat_id":"-100"}`, true},
		{"missing chat", `{"bot_token":"123:ABC"}`, true},
		{"empty token", `{"bot_token":"","chat_id":"-100"}`, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, err := buildTelegramRequest(sampleNote, mustDecode(t, tc.cfg))
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
			// The bot token is a path segment, not a query param or header.
			if want := "https://api.telegram.org/bot123:ABC/sendMessage"; req.URL != want {
				t.Errorf("url = %q, want %q", req.URL, want)
			}
			var payload map[string]any
			if err := json.Unmarshal(req.Body, &payload); err != nil {
				t.Fatalf("body not valid json: %v", err)
			}
			if payload["chat_id"] != "-1001234567890" {
				t.Errorf("chat_id = %v", payload["chat_id"])
			}
			if payload["parse_mode"] != "HTML" {
				t.Errorf("parse_mode = %v, want HTML", payload["parse_mode"])
			}
			text, _ := payload["text"].(string)
			for _, want := range []string{"Blacklist hit", "example.com", "CRITICAL", "sentinel.example.com/incidents"} {
				if !strings.Contains(text, want) {
					t.Errorf("text missing %q:\n%s", want, text)
				}
			}
			if _, ok := payload["message_thread_id"]; ok {
				t.Errorf("message_thread_id should be absent when unconfigured")
			}
		})
	}
}

func TestBuildTelegramRequestOptions(t *testing.T) {
	req, err := buildTelegramRequest(sampleNote, mustDecode(t,
		`{"bot_token":"t","chat_id":"@ops","message_thread_id":"42","api_base":"http://localhost:9/"}`))
	if err != nil {
		t.Fatal(err)
	}
	if want := "http://localhost:9/bott/sendMessage"; req.URL != want {
		t.Errorf("url = %q, want %q", req.URL, want)
	}
	var payload map[string]any
	if err := json.Unmarshal(req.Body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["message_thread_id"] != "42" {
		t.Errorf("message_thread_id = %v, want 42", payload["message_thread_id"])
	}
	if payload["chat_id"] != "@ops" {
		t.Errorf("chat_id = %v, want @ops", payload["chat_id"])
	}
}

// A value containing HTML metacharacters must be escaped: parse_mode=HTML makes Telegram
// reject the whole message with 400 if it sees an unknown tag.
func TestTelegramTextEscapesHTML(t *testing.T) {
	n := sampleNote
	n.Title = `Rejected <b>by</b> "MTA" & co`
	n.Domain = "<script>alert(1)</script>"
	text := telegramText(n)
	if strings.Contains(text, "<script>") || strings.Contains(text, "<b>by</b>") {
		t.Errorf("unescaped markup survived:\n%s", text)
	}
	if !strings.Contains(text, "&lt;script&gt;") {
		t.Errorf("domain not escaped:\n%s", text)
	}
	// Our own markup is still intact.
	if !strings.Contains(text, "<b>Rejected") {
		t.Errorf("title should still be wrapped in <b>:\n%s", text)
	}
}

func TestTelegramTestPrefix(t *testing.T) {
	n := sampleNote
	n.Test = true
	if !strings.Contains(telegramText(n), "[TEST]") {
		t.Errorf("test notification should be prefixed")
	}
}

func TestTelegramNotifierSendsThroughTransport(t *testing.T) {
	var got *HTTPRequest
	n := TelegramNotifier{HTTP: doerFunc(func(_ context.Context, req *HTTPRequest) error {
		got = req
		return nil
	})}
	if err := n.Send(context.Background(), sampleNote, mustDecode(t, `{"bot_token":"t","chat_id":"1"}`)); err != nil {
		t.Fatalf("send: %v", err)
	}
	if got == nil || !strings.HasSuffix(got.URL, "/sendMessage") {
		t.Fatalf("driver did not post to sendMessage: %+v", got)
	}
	if n.Type() != TypeTelegram {
		t.Errorf("Type() = %q, want %q", n.Type(), TypeTelegram)
	}
}

// The bot token is a secret and must be sealed at rest like every other channel secret.
func TestTelegramSecretFields(t *testing.T) {
	fields := SecretFields(TypeTelegram)
	if len(fields) != 1 || fields[0] != "bot_token" {
		t.Fatalf("SecretFields(telegram) = %v, want [bot_token]", fields)
	}
	redacted, err := RedactConfig(TypeTelegram, []byte(`{"bot_token":"123:ABC","chat_id":"-100"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(redacted), "123:ABC") {
		t.Errorf("bot token leaked through RedactConfig: %s", redacted)
	}
	if !strings.Contains(string(redacted), `"chat_id":"-100"`) {
		t.Errorf("chat_id should not be redacted: %s", redacted)
	}
}

func TestTelegramInRegistry(t *testing.T) {
	if _, ok := NewRegistry(Config{HTTPTimeout: time.Second})[TypeTelegram]; !ok {
		t.Errorf("registry has no telegram driver")
	}
	if !ValidTypes[TypeTelegram] {
		t.Errorf("telegram is not an accepted channel type")
	}
}

// doerFunc adapts a function to the HTTPDoer interface.
type doerFunc func(context.Context, *HTTPRequest) error

func (f doerFunc) Do(ctx context.Context, req *HTTPRequest) error { return f(ctx, req) }
