package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/zezekim/mxsentinel/pkg/contracts"
)

// A maillog-sourced delivery carries no SPF/DKIM/DMARC verdict and an empty bounce_class.
// Those map to ClickHouse Enum8 columns where "" is not a valid element, so rowFromEnvelope
// must substitute "none" — otherwise the whole insert batch is rejected (the bug that left
// the Message Explorer empty).
func TestRowFromEnvelopeDefaultsEmptyEnums(t *testing.T) {
	raw, err := json.Marshal(contracts.SMTPPayload{Outcome: "delivered", FromDomain: "example.com"})
	if err != nil {
		t.Fatal(err)
	}
	ev := &contracts.Envelope{
		EventID:    "0190000000007a1b8c00000000000001",
		OccurredAt: time.Now().UTC().Format(time.RFC3339Nano),
		IngestedAt: time.Now().UTC().Format(time.RFC3339Nano),
		TenantID:   "11111111-1111-1111-1111-111111111111",
		Payload:    raw,
	}
	row, ok := rowFromEnvelope(ev)
	if !ok {
		t.Fatal("expected a row")
	}
	for name, got := range map[string]string{
		"spf_result":   row.SPFResult,
		"dkim_result":  row.DKIMResult,
		"dmarc_result": row.DMARCResult,
		"bounce_class": row.BounceClass,
	} {
		if got != "none" {
			t.Errorf("%s = %q, want \"none\"", name, got)
		}
	}
	if row.EventType != "delivered" {
		t.Errorf("event_type = %q, want \"delivered\"", row.EventType)
	}
}

func TestEnumOrNone(t *testing.T) {
	if got := enumOrNone(""); got != "none" {
		t.Errorf(`enumOrNone("") = %q, want "none"`, got)
	}
	if got := enumOrNone("pass"); got != "pass" {
		t.Errorf(`enumOrNone("pass") = %q, want "pass"`, got)
	}
}
