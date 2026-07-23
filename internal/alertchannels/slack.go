package alertchannels

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// SlackNotifier delivers to a Slack incoming webhook.
type SlackNotifier struct {
	HTTP HTTPDoer
}

func (n SlackNotifier) Type() string { return TypeSlack }

func (n SlackNotifier) Send(ctx context.Context, note Notification, cfg map[string]any) error {
	req, err := buildSlackRequest(note, cfg)
	if err != nil {
		return err
	}
	return n.HTTP.Do(ctx, req)
}

// slackColor maps severity to a Slack attachment color bar.
func slackColor(sev string) string {
	switch severityRank(sev) {
	case 3:
		return "#d64545" // red
	case 1:
		return "#3d8f5b" // green
	default:
		return "#d9a441" // amber
	}
}

// buildSlackRequest renders a Notification into a Slack incoming-webhook POST. Pure: no I/O.
// Config: {"webhook_url": "https://hooks.slack.com/services/..."}.
func buildSlackRequest(note Notification, cfg map[string]any) (*HTTPRequest, error) {
	url := cfgString(cfg, "webhook_url")
	if url == "" {
		return nil, fmt.Errorf("slack: webhook_url is required")
	}

	var fields []map[string]any
	if note.Domain != "" {
		fields = append(fields, map[string]any{"title": "Domain", "value": note.Domain, "short": true})
	}
	if note.Kind != "" {
		fields = append(fields, map[string]any{"title": "Kind", "value": note.Kind, "short": true})
	}
	fields = append(fields, map[string]any{"title": "Severity", "value": strings.ToUpper(note.Severity), "short": true})

	header := note.Title
	if note.Test {
		header = "[TEST] " + header
	}

	att := map[string]any{
		"color":  slackColor(note.Severity),
		"title":  header,
		"text":   note.Summary,
		"fields": fields,
		"footer": "MX Sentinel",
	}
	if note.LinkURL != "" {
		att["title_link"] = note.LinkURL
	}
	if !note.OccurredAt.IsZero() {
		att["ts"] = note.OccurredAt.Unix()
	}

	payload := map[string]any{
		"text":        header,
		"attachments": []any{att},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("slack: marshal payload: %w", err)
	}
	return &HTTPRequest{
		Method: "POST",
		URL:    url,
		Header: map[string]string{"Content-Type": "application/json"},
		Body:   body,
	}, nil
}
