package alertchannels

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"strings"
)

// telegramAPIBase is the Bot API origin. Overridable per channel via the (undocumented,
// test/self-host only) "api_base" config key so tests never touch the network.
const telegramAPIBase = "https://api.telegram.org"

// TelegramNotifier delivers to a Telegram chat via the Bot API sendMessage method.
type TelegramNotifier struct {
	HTTP HTTPDoer
}

func (n TelegramNotifier) Type() string { return TypeTelegram }

func (n TelegramNotifier) Send(ctx context.Context, note Notification, cfg map[string]any) error {
	req, err := buildTelegramRequest(note, cfg)
	if err != nil {
		return err
	}
	return n.HTTP.Do(ctx, req)
}

// telegramEmoji maps severity to a leading glyph, so a phone notification is triageable
// from the lock screen without opening the app.
func telegramEmoji(sev string) string {
	switch severityRank(sev) {
	case 3:
		return "\U0001F534" // red circle
	case 1:
		return "\U0001F535" // blue circle
	default:
		return "\U0001F7E1" // yellow circle
	}
}

// buildTelegramRequest renders a Notification into a Bot API sendMessage POST. Pure: no I/O.
// Config: {"bot_token": "123456:ABC…", "chat_id": "-1001234567890"} plus the optional
// "message_thread_id" for a forum-group topic.
//
// chat_id is sent as a string; the Bot API accepts both the numeric id and an "@channel"
// username in that form, so we never have to guess which one the operator pasted.
func buildTelegramRequest(note Notification, cfg map[string]any) (*HTTPRequest, error) {
	token := cfgString(cfg, "bot_token")
	if token == "" {
		return nil, fmt.Errorf("telegram: bot_token is required")
	}
	chatID := cfgString(cfg, "chat_id")
	if chatID == "" {
		return nil, fmt.Errorf("telegram: chat_id is required")
	}

	payload := map[string]any{
		"chat_id":                  chatID,
		"text":                     telegramText(note),
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	}
	if thread := cfgString(cfg, "message_thread_id"); thread != "" {
		payload["message_thread_id"] = thread
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("telegram: marshal payload: %w", err)
	}

	base := strings.TrimRight(cfgString(cfg, "api_base"), "/")
	if base == "" {
		base = telegramAPIBase
	}
	return &HTTPRequest{
		Method: "POST",
		// The token is a URL path segment, so it must be escaped even though real tokens
		// are alphanumeric + ':'.
		URL:    base + "/bot" + url.PathEscape(token) + "/sendMessage",
		Header: map[string]string{"Content-Type": "application/json"},
		Body:   body,
	}, nil
}

// telegramText renders the HTML message body. Every interpolated value is HTML-escaped —
// a domain or summary containing "<" must not break the parse_mode=HTML render (Telegram
// rejects the whole message with 400 if it does).
func telegramText(note Notification) string {
	title := note.Title
	if note.Test {
		title = "[TEST] " + title
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s <b>%s</b>\n", telegramEmoji(note.Severity), html.EscapeString(title))
	if note.Summary != "" {
		fmt.Fprintf(&b, "%s\n", html.EscapeString(note.Summary))
	}
	b.WriteString("\n")
	if note.Domain != "" {
		fmt.Fprintf(&b, "Domain: <code>%s</code>\n", html.EscapeString(note.Domain))
	}
	if note.Kind != "" {
		fmt.Fprintf(&b, "Kind: %s\n", html.EscapeString(note.Kind))
	}
	fmt.Fprintf(&b, "Severity: %s\n", html.EscapeString(strings.ToUpper(note.Severity)))
	if !note.OccurredAt.IsZero() {
		fmt.Fprintf(&b, "When: %s\n", note.OccurredAt.UTC().Format("2006-01-02 15:04:05 MST"))
	}
	if note.LinkURL != "" {
		fmt.Fprintf(&b, "\n<a href=\"%s\">Open in MX Sentinel</a>", html.EscapeString(note.LinkURL))
	}
	return strings.TrimRight(b.String(), "\n")
}
