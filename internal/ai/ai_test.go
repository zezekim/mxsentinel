package ai

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeClient struct {
	resp string
	err  error
}

func (f fakeClient) Complete(context.Context, string, string) (string, error) {
	return f.resp, f.err
}

const goodJSON = `{"narrative":"DKIM selector selector2 was removed, breaking authentication at Microsoft.","confidence":0.9,"recommendations":[{"action":"restore_dkim_selector","summary":"Restore selector2._domainkey.example.com","target":"selector2._domainkey.example.com","priority":"urgent"}]}`

func TestParseDiagnosisVariants(t *testing.T) {
	cases := map[string]string{
		"plain":  goodJSON,
		"fenced": "```json\n" + goodJSON + "\n```",
		"prose":  "Sure, here is my analysis:\n" + goodJSON + "\nHope that helps!",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			d, err := ParseDiagnosis(raw)
			if err != nil {
				t.Fatalf("ParseDiagnosis: %v", err)
			}
			if d.Confidence != 0.9 || len(d.Recommendations) != 1 {
				t.Errorf("bad parse: %+v", d)
			}
			if d.Recommendations[0].Action != "restore_dkim_selector" {
				t.Errorf("recommendation lost: %+v", d.Recommendations[0])
			}
		})
	}
}

func TestParseDiagnosisInvalid(t *testing.T) {
	if _, err := ParseDiagnosis("no json here"); err == nil {
		t.Error("expected error for non-JSON response")
	}
	if _, err := ParseDiagnosis(`{"confidence":0.5}`); err == nil {
		t.Error("expected error when narrative is missing")
	}
}

func TestDiagnose(t *testing.T) {
	in := Incident{SourceEventID: "ev-1", Kind: "rejection_spike", Severity: "critical",
		Domain: "example.com", Title: "Rejection spike at microsoft"}

	p, err := Diagnose(context.Background(), fakeClient{resp: goodJSON}, in, "llama3")
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if p.Kind != "rca" || p.Title != in.Title || p.Model != "llama3" {
		t.Errorf("bad payload: %+v", p)
	}
	if p.Confidence != 0.9 || len(p.Recommendations) != 1 {
		t.Errorf("bad payload fields: %+v", p)
	}
	if len(p.EvidenceEventIDs) != 1 || p.EvidenceEventIDs[0] != "ev-1" {
		t.Errorf("evidence not linked: %+v", p.EvidenceEventIDs)
	}
}

func TestDiagnoseClampsConfidence(t *testing.T) {
	resp := `{"narrative":"x","confidence":1.7,"recommendations":[]}`
	p, err := Diagnose(context.Background(), fakeClient{resp: resp}, Incident{Title: "t"}, "m")
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if p.Confidence != 1.0 {
		t.Errorf("confidence not clamped: %v", p.Confidence)
	}
}

func TestDiagnoseClientError(t *testing.T) {
	_, err := Diagnose(context.Background(), fakeClient{err: errors.New("connection refused")}, Incident{Title: "t"}, "m")
	if err == nil {
		t.Error("expected error when client fails")
	}
}

func TestBuildUserPromptIsMetadataOnly(t *testing.T) {
	in := Incident{Kind: "dns_validation", Domain: "example.com", Title: "DNS issues"}
	prompt := BuildUserPrompt(in)
	if !strings.Contains(prompt, "example.com") || !strings.Contains(prompt, "DNS issues") {
		t.Errorf("prompt missing metadata: %q", prompt)
	}
}
