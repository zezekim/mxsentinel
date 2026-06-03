// Package ai is the Phase 3 reasoning layer. It turns an incident's METADATA (never
// message bodies — see ARCHITECTURE.md §5) into a root-cause narrative + structured
// remediation by calling a local, OpenAI-compatible LLM (Ollama / vLLM / llama.cpp).
// Prompt building and response parsing are pure and unit-tested; the HTTP client is an
// interface so callers can mock it.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/zezekim/mxsentinel/pkg/contracts"
)

// Client is a minimal chat-completion interface (OpenAI-compatible).
type Client interface {
	Complete(ctx context.Context, system, user string) (string, error)
}

// Incident is the metadata the diagnostic prompt reasons over. No message content.
type Incident struct {
	SourceEventID string          `json:"-"`
	Kind          string          `json:"kind"`
	Severity      string          `json:"severity"`
	Domain        string          `json:"domain,omitempty"`
	Subject       string          `json:"subject,omitempty"`
	Title         string          `json:"title"`
	Detail        json.RawMessage `json:"detail,omitempty"`
	Confidence    *float64        `json:"correlation_confidence,omitempty"`
	// Context carries recent historical signal (deliverability stats, prior incidents)
	// so the model can reason in context. Populated by the caller; metadata only.
	Context map[string]any `json:"context,omitempty"`
}

// Diagnosis is the parsed structured LLM result.
type Diagnosis struct {
	Narrative       string                       `json:"narrative"`
	Confidence      float64                      `json:"confidence"`
	Recommendations []contracts.AIRecommendation `json:"recommendations"`
}

const systemPrompt = `You are MX Sentinel's email-deliverability diagnostic engine, assisting mail operators.
You are given METADATA about a detected incident (DNS validation findings, rejection-spike correlations, or blocklist hits). You never see message contents.
Produce a concise, operational root-cause explanation and concrete remediation steps.
Respond with ONLY a single JSON object, no prose, in exactly this shape:
{"narrative": string, "confidence": number between 0 and 1, "recommendations": [{"action": string, "summary": string, "target": string, "priority": "low"|"medium"|"high"|"urgent"}]}
Reason only from the metadata provided; do not invent facts.`

// BuildUserPrompt renders the incident metadata into the user message.
func BuildUserPrompt(in Incident) string {
	b, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		b = []byte(fmt.Sprintf("%+v", in))
	}
	return "Incident metadata:\n" + string(b) + "\n\nDiagnose the most likely root cause and the remediation steps."
}

// ParseDiagnosis extracts and parses the JSON object from a (possibly prose- or
// code-fence-wrapped) LLM response.
func ParseDiagnosis(raw string) (Diagnosis, error) {
	jsonText := extractJSONObject(raw)
	if jsonText == "" {
		return Diagnosis{}, fmt.Errorf("no JSON object found in response")
	}
	var d Diagnosis
	if err := json.Unmarshal([]byte(jsonText), &d); err != nil {
		return Diagnosis{}, fmt.Errorf("parse diagnosis json: %w", err)
	}
	if strings.TrimSpace(d.Narrative) == "" {
		return Diagnosis{}, fmt.Errorf("diagnosis missing narrative")
	}
	return d, nil
}

// Diagnose runs the LLM and returns an ai.rca payload plus the parsed diagnosis.
func Diagnose(ctx context.Context, c Client, in Incident, model string) (contracts.AIPayload, error) {
	out, err := c.Complete(ctx, systemPrompt, BuildUserPrompt(in))
	if err != nil {
		return contracts.AIPayload{}, fmt.Errorf("llm complete: %w", err)
	}
	d, err := ParseDiagnosis(out)
	if err != nil {
		return contracts.AIPayload{}, err
	}
	return contracts.AIPayload{
		Kind:             "rca",
		Title:            in.Title,
		Narrative:        d.Narrative,
		Confidence:       clamp01(d.Confidence),
		Recommendations:  d.Recommendations,
		EvidenceEventIDs: []string{in.SourceEventID},
		Model:            model,
	}, nil
}

// extractJSONObject returns the substring from the first '{' to the last '}', stripping
// any markdown code fences first. Returns "" if no braces are present.
func extractJSONObject(s string) string {
	s = strings.ReplaceAll(s, "```json", "")
	s = strings.ReplaceAll(s, "```", "")
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start < 0 || end < start {
		return ""
	}
	return s[start : end+1]
}

func clamp01(f float64) float64 {
	switch {
	case f < 0:
		return 0
	case f > 1:
		return 1
	default:
		return f
	}
}

// ----------------------------------------------------------------------------
// OpenAI-compatible HTTP client
// ----------------------------------------------------------------------------

// OpenAIClient calls an OpenAI-compatible /chat/completions endpoint.
type OpenAIClient struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
}

// NewOpenAIClient builds a client. baseURL is the API base (e.g. http://localhost:11434/v1).
func NewOpenAIClient(baseURL, apiKey, model string, timeout time.Duration) *OpenAIClient {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &OpenAIClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
		http:    &http.Client{Timeout: timeout},
	}
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	Stream      bool          `json:"stream"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Complete sends a system+user prompt and returns the assistant message content.
func (c *OpenAIClient) Complete(ctx context.Context, system, user string) (string, error) {
	reqBody, err := json.Marshal(chatRequest{
		Model: c.model,
		Messages: []chatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		Temperature: 0.2,
		Stream:      false,
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("llm endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var cr chatResponse
	if err := json.Unmarshal(body, &cr); err != nil {
		return "", fmt.Errorf("decode llm response: %w", err)
	}
	if cr.Error != nil {
		return "", fmt.Errorf("llm error: %s", cr.Error.Message)
	}
	if len(cr.Choices) == 0 {
		return "", fmt.Errorf("llm returned no choices")
	}
	return cr.Choices[0].Message.Content, nil
}
