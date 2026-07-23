package nlquery

import (
	"strings"
	"testing"
	"time"
)

// TestWhitelistIsAggregateOnly asserts the privacy invariant: no whitelisted tool exposes a
// parameter that could carry message content (body/subject/etc.), and every tool's result
// columns are aggregate/categorical — never a raw content field.
func TestWhitelistIsAggregateOnly(t *testing.T) {
	if len(Registry) == 0 {
		t.Fatal("registry is empty")
	}
	forbiddenColumns := map[string]bool{
		"body": true, "subject": true, "message_body": true, "subject_line": true,
		"content": true, "text": true, "raw": true, "headers": true, "html": true,
		"response_text": true, // even the remote SMTP text is not surfaced by NL tools
	}
	for _, tool := range Registry {
		for _, p := range tool.Params {
			if forbiddenParamNames[strings.ToLower(p.Name)] {
				t.Errorf("tool %q declares forbidden param %q", tool.Name, p.Name)
			}
		}
		// Execute against a fake executor and confirm no forbidden columns appear.
		res, err := tool.run(tContext(), fakeExecutor{}, "tenant-1", validSampleArgs(tool))
		if err != nil {
			t.Errorf("tool %q run: %v", tool.Name, err)
			continue
		}
		for _, c := range res.Columns {
			if forbiddenColumns[strings.ToLower(c)] {
				t.Errorf("tool %q returns forbidden column %q", tool.Name, c)
			}
		}
	}
}

func TestValidateArgs(t *testing.T) {
	deliver, _ := ToolByName("deliverability_by_provider")
	senders, _ := ToolByName("top_senders")

	tests := []struct {
		name    string
		tool    Tool
		args    map[string]any
		wantErr bool
	}{
		{"valid window", deliver, map[string]any{"window": "24h"}, false},
		{"valid explicit range", deliver, map[string]any{
			"since": "2026-07-14T00:00:00Z", "until": "2026-07-15T00:00:00Z"}, false},
		{"bad enum window", deliver, map[string]any{"window": "Tuesday"}, true},
		{"bad time", deliver, map[string]any{"since": "yesterday"}, true},
		{"unknown arg rejected", deliver, map[string]any{"provider": "yahoo"}, true},
		{"forbidden body arg rejected", deliver, map[string]any{"body": "hello"}, true},
		{"forbidden subject arg rejected", deliver, map[string]any{"subject": "re: hi"}, true},
		{"missing required dimension", senders, map[string]any{"metric": "volume"}, true},
		{"valid top senders", senders, map[string]any{"dimension": "ip", "metric": "volume"}, false},
		{"bad enum metric", senders, map[string]any{"dimension": "ip", "metric": "opens"}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidateArgs(tc.tool, tc.args)
			if tc.wantErr && err == nil {
				t.Errorf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateArgsNormalizes(t *testing.T) {
	senders, _ := ToolByName("top_senders")
	out, err := ValidateArgs(senders, map[string]any{
		"dimension": "IP", "metric": "Volume", "limit": 999, // enum case + clamp
	})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if out["dimension"] != "ip" || out["metric"] != "volume" {
		t.Errorf("enums not lower-cased: %+v", out)
	}
	if out["limit"].(int) != 100 { // clamped to Max
		t.Errorf("limit not clamped to 100: %v", out["limit"])
	}
}

func TestResolveWindow(t *testing.T) {
	// explicit since wins over window
	since, until := resolveWindow(map[string]any{
		"window": "1h",
		"since":  time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC),
		"until":  time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC),
	}, 7*24*time.Hour)
	if since.Year() != 2026 || since.Month() != 7 || since.Day() != 14 {
		t.Errorf("since not honored: %v", since)
	}
	if until.Day() != 15 {
		t.Errorf("until not honored: %v", until)
	}

	// window resolves to a lookback
	since2, _ := resolveWindow(map[string]any{"window": "24h"}, 7*24*time.Hour)
	if time.Since(since2) < 23*time.Hour || time.Since(since2) > 25*time.Hour {
		t.Errorf("24h window not resolved: %v", since2)
	}

	// default applies when nothing given
	since3, _ := resolveWindow(map[string]any{}, 7*24*time.Hour)
	if since3.IsZero() {
		t.Errorf("default window not applied")
	}
}

// validSampleArgs supplies minimal valid args so every tool's run func can execute.
func validSampleArgs(t Tool) map[string]any {
	switch t.Name {
	case "top_senders":
		return map[string]any{"dimension": "ip", "metric": "volume"}
	default:
		return map[string]any{}
	}
}
