package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zezekim/mxsentinel/pkg/contracts"
)

// systemAnomalyPrompt instructs the model to classify a deliverability window.
const systemAnomalyPrompt = `You are MX Sentinel's deliverability anomaly classifier.
You are given METADATA describing a recent mail-flow window (no message bodies).
Classify whether the window shows an anomaly.
Allowed classifications: none | spam_outbreak | reputation_decline | provider_throttling | config_regression | volume_spike | other
"none" means the window is not anomalous.
Respond with ONLY a single JSON object, no prose, in exactly this shape:
{"classification": string, "severity": "info"|"warning"|"critical", "narrative": string, "confidence": number between 0 and 1}
Reason only from the metadata provided; do not invent facts.`

// AnomalyInput is the recent-window metadata the classifier reasons over (no bodies).
type AnomalyInput struct {
	Domain              string         `json:"domain,omitempty"`
	Provider            string         `json:"provider,omitempty"`
	Window              string         `json:"window,omitempty"`
	Stats               map[string]any `json:"stats,omitempty"`
	TopRejectionReasons []string       `json:"top_rejection_reasons,omitempty"`
}

// Anomaly is the parsed classification.
type Anomaly struct {
	Classification string  `json:"classification"` // none|spam_outbreak|reputation_decline|provider_throttling|config_regression|volume_spike|other
	Severity       string  `json:"severity"`       // info|warning|critical
	Narrative      string  `json:"narrative"`
	Confidence     float64 `json:"confidence"`
}

// BuildAnomalyPrompt json.MarshalIndents the AnomalyInput and wraps it in a
// standardised user message.
func BuildAnomalyPrompt(in AnomalyInput) string {
	b, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		b = []byte(fmt.Sprintf("%+v", in))
	}
	return "Recent deliverability window:\n" + string(b) + "\n\nClassify any anomaly."
}

// ParseAnomaly extracts and parses the JSON object from a (possibly prose- or
// code-fence-wrapped) LLM response.
func ParseAnomaly(raw string) (Anomaly, error) {
	jsonText := extractJSONObject(raw)
	if jsonText == "" {
		return Anomaly{}, fmt.Errorf("no JSON object found in response")
	}
	var a Anomaly
	if err := json.Unmarshal([]byte(jsonText), &a); err != nil {
		return Anomaly{}, fmt.Errorf("parse anomaly json: %w", err)
	}
	if strings.TrimSpace(a.Classification) == "" {
		a.Classification = "none"
	}
	switch a.Severity {
	case "info", "warning", "critical":
		// valid — keep it
	default:
		a.Severity = "warning"
	}
	return a, nil
}

// ClassifyAnomaly calls the LLM and returns a contracts.AIPayload plus the
// parsed Anomaly. The payload Kind is always "anomaly".
func ClassifyAnomaly(ctx context.Context, c Client, in AnomalyInput, model string) (contracts.AIPayload, Anomaly, error) {
	raw, err := c.Complete(ctx, systemAnomalyPrompt, BuildAnomalyPrompt(in))
	if err != nil {
		return contracts.AIPayload{}, Anomaly{}, fmt.Errorf("llm complete: %w", err)
	}
	a, err := ParseAnomaly(raw)
	if err != nil {
		return contracts.AIPayload{}, Anomaly{}, err
	}

	title := buildAnomalyTitle(a.Classification, in.Domain, in.Provider)

	payload := contracts.AIPayload{
		Kind:       "anomaly",
		Title:      title,
		Narrative:  a.Narrative,
		Confidence: clamp01(a.Confidence),
		Model:      model,
	}
	return payload, a, nil
}

// buildAnomalyTitle composes a human-readable title from classification +
// domain/provider context, e.g. "spam_outbreak anomaly for example.com".
func buildAnomalyTitle(classification, domain, provider string) string {
	label := classification
	if label == "" {
		label = "anomaly"
	}

	switch {
	case domain != "" && provider != "":
		return fmt.Sprintf("%s anomaly for %s at %s", label, domain, provider)
	case domain != "":
		return fmt.Sprintf("%s anomaly for %s", label, domain)
	case provider != "":
		return fmt.Sprintf("%s anomaly at %s", label, provider)
	default:
		return fmt.Sprintf("%s anomaly", label)
	}
}
