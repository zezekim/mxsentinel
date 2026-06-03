package ai

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeClient is already declared in ai_test.go; reused here in the same package.

const goodAnomalyJSON = `{"classification":"reputation_decline","severity":"warning","narrative":"Rejection rate rose 18 % above baseline over the last hour.","confidence":0.7}`

func TestParseAnomaly(t *testing.T) {
	t.Run("valid_plain", func(t *testing.T) {
		a, err := ParseAnomaly(goodAnomalyJSON)
		if err != nil {
			t.Fatalf("ParseAnomaly: %v", err)
		}
		if a.Classification != "reputation_decline" {
			t.Errorf("classification: got %q want reputation_decline", a.Classification)
		}
		if a.Severity != "warning" {
			t.Errorf("severity: got %q want warning", a.Severity)
		}
		if a.Confidence != 0.7 {
			t.Errorf("confidence: got %v want 0.7", a.Confidence)
		}
	})

	t.Run("fenced", func(t *testing.T) {
		raw := "```json\n" + goodAnomalyJSON + "\n```"
		a, err := ParseAnomaly(raw)
		if err != nil {
			t.Fatalf("ParseAnomaly fenced: %v", err)
		}
		if a.Classification != "reputation_decline" {
			t.Errorf("classification: got %q", a.Classification)
		}
	})

	t.Run("non_json_returns_error", func(t *testing.T) {
		if _, err := ParseAnomaly("no json here at all"); err == nil {
			t.Error("expected error for non-JSON response")
		}
	})

	t.Run("empty_classification_defaults_to_none", func(t *testing.T) {
		raw := `{"severity":"info","narrative":"all good","confidence":0.1}`
		a, err := ParseAnomaly(raw)
		if err != nil {
			t.Fatalf("ParseAnomaly: %v", err)
		}
		if a.Classification != "none" {
			t.Errorf("expected classification=none, got %q", a.Classification)
		}
	})

	t.Run("invalid_severity_defaults_to_warning", func(t *testing.T) {
		raw := `{"classification":"volume_spike","severity":"catastrophic","narrative":"big spike","confidence":0.8}`
		a, err := ParseAnomaly(raw)
		if err != nil {
			t.Fatalf("ParseAnomaly: %v", err)
		}
		if a.Severity != "warning" {
			t.Errorf("expected severity=warning, got %q", a.Severity)
		}
	})
}

func TestClassifyAnomaly(t *testing.T) {
	in := AnomalyInput{
		Domain:   "example.com",
		Provider: "microsoft",
		Window:   "1h",
		Stats:    map[string]any{"rejection_rate": 0.18},
	}

	t.Run("success", func(t *testing.T) {
		fc := fakeClient{resp: goodAnomalyJSON}
		payload, anomaly, err := ClassifyAnomaly(context.Background(), fc, in, "llama3")
		if err != nil {
			t.Fatalf("ClassifyAnomaly: %v", err)
		}
		if payload.Kind != "anomaly" {
			t.Errorf("Kind: got %q want anomaly", payload.Kind)
		}
		if payload.Title == "" {
			t.Error("Title should not be empty")
		}
		if !strings.Contains(payload.Title, "example.com") {
			t.Errorf("Title should contain domain: %q", payload.Title)
		}
		if payload.Confidence < 0 || payload.Confidence > 1 {
			t.Errorf("Confidence not clamped: %v", payload.Confidence)
		}
		if anomaly.Classification != "reputation_decline" {
			t.Errorf("Anomaly.Classification: got %q want reputation_decline", anomaly.Classification)
		}
		if payload.Model != "llama3" {
			t.Errorf("Model: got %q want llama3", payload.Model)
		}
	})

	t.Run("confidence_clamped_above_1", func(t *testing.T) {
		resp := `{"classification":"volume_spike","severity":"critical","narrative":"huge spike","confidence":1.9}`
		fc := fakeClient{resp: resp}
		payload, _, err := ClassifyAnomaly(context.Background(), fc, in, "m")
		if err != nil {
			t.Fatalf("ClassifyAnomaly: %v", err)
		}
		if payload.Confidence != 1.0 {
			t.Errorf("confidence not clamped: %v", payload.Confidence)
		}
	})

	t.Run("client_error", func(t *testing.T) {
		fc := fakeClient{err: errors.New("connection refused")}
		_, _, err := ClassifyAnomaly(context.Background(), fc, in, "m")
		if err == nil {
			t.Error("expected error when client fails")
		}
	})

	t.Run("title_domain_only", func(t *testing.T) {
		inDomainOnly := AnomalyInput{Domain: "acme.org"}
		fc := fakeClient{resp: goodAnomalyJSON}
		payload, _, err := ClassifyAnomaly(context.Background(), fc, inDomainOnly, "m")
		if err != nil {
			t.Fatalf("ClassifyAnomaly: %v", err)
		}
		if !strings.Contains(payload.Title, "acme.org") {
			t.Errorf("Title missing domain: %q", payload.Title)
		}
	})

	t.Run("title_provider_only", func(t *testing.T) {
		inProviderOnly := AnomalyInput{Provider: "google"}
		fc := fakeClient{resp: goodAnomalyJSON}
		payload, _, err := ClassifyAnomaly(context.Background(), fc, inProviderOnly, "m")
		if err != nil {
			t.Fatalf("ClassifyAnomaly: %v", err)
		}
		if !strings.Contains(payload.Title, "google") {
			t.Errorf("Title missing provider: %q", payload.Title)
		}
	})

	t.Run("title_no_context", func(t *testing.T) {
		fc := fakeClient{resp: goodAnomalyJSON}
		payload, _, err := ClassifyAnomaly(context.Background(), fc, AnomalyInput{}, "m")
		if err != nil {
			t.Fatalf("ClassifyAnomaly: %v", err)
		}
		if payload.Title == "" {
			t.Error("Title should not be empty even with no domain/provider")
		}
	})
}
