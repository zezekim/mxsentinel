package alertchannels

import (
	"context"
	"encoding/json"
	"fmt"
)

// pagerDutyEventsURL is the PagerDuty Events API v2 enqueue endpoint.
const pagerDutyEventsURL = "https://events.pagerduty.com/v2/enqueue"

// PagerDutyNotifier delivers to PagerDuty via the Events API v2.
type PagerDutyNotifier struct {
	HTTP HTTPDoer
}

func (n PagerDutyNotifier) Type() string { return TypePagerDuty }

func (n PagerDutyNotifier) Send(ctx context.Context, note Notification, cfg map[string]any) error {
	req, err := buildPagerDutyRequest(note, cfg)
	if err != nil {
		return err
	}
	return n.HTTP.Do(ctx, req)
}

// pagerDutySeverity maps our severity labels to PagerDuty's allowed values
// (critical | error | warning | info).
func pagerDutySeverity(sev string) string {
	switch severityRank(sev) {
	case 3:
		return "critical"
	case 1:
		return "info"
	default:
		return "warning"
	}
}

// buildPagerDutyRequest renders a Notification into a PagerDuty Events API v2 "trigger"
// event. Pure: no I/O. Config: {"routing_key": "<32-char integration key>"}.
//
// dedup_key is set to the AlertRef so PagerDuty collapses repeats of the same incident into
// one alert on its side as well.
func buildPagerDutyRequest(note Notification, cfg map[string]any) (*HTTPRequest, error) {
	routingKey := cfgString(cfg, "routing_key")
	if routingKey == "" {
		return nil, fmt.Errorf("pagerduty: routing_key is required")
	}

	summary := note.Title
	if note.Summary != "" {
		summary = note.Title + " — " + note.Summary
	}
	if note.Test {
		summary = "[TEST] " + summary
	}

	source := note.Domain
	if source == "" {
		source = "mxsentinel"
	}

	custom := map[string]any{
		"kind":      note.Kind,
		"domain":    note.Domain,
		"alert_ref": note.AlertRef,
	}

	payload := map[string]any{
		"routing_key":  routingKey,
		"event_action": "trigger",
		"dedup_key":    note.AlertRef,
		"payload": map[string]any{
			"summary":        summary,
			"source":         source,
			"severity":       pagerDutySeverity(note.Severity),
			"component":      "email-infrastructure",
			"group":          note.Kind,
			"custom_details": custom,
		},
	}
	if note.LinkURL != "" {
		payload["links"] = []any{
			map[string]any{"href": note.LinkURL, "text": "View in MX Sentinel"},
		}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("pagerduty: marshal payload: %w", err)
	}
	return &HTTPRequest{
		Method: "POST",
		URL:    pagerDutyEventsURL,
		Header: map[string]string{"Content-Type": "application/json"},
		Body:   body,
	}, nil
}
