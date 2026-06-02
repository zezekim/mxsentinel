package events

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/zezekim/mxsentinel/pkg/contracts"
)

func newTestValidator(t *testing.T) *Validator {
	t.Helper()
	v, err := NewValidator()
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	return v
}

func TestValidatorLoadsAllFamilies(t *testing.T) {
	v := newTestValidator(t)
	got := map[string]bool{}
	for _, f := range v.Families() {
		got[f] = true
	}
	for _, want := range []string{"smtp", "dns", "reputation", "ai"} {
		if !got[want] {
			t.Errorf("missing compiled schema for family %q (have %v)", want, v.Families())
		}
	}
}

// baseValid returns a valid smtp.delivered envelope as a mutable map.
func baseValid(t *testing.T) map[string]any {
	t.Helper()
	ev, err := NewEnvelope(contracts.EventSMTPDelivered, "tenant-1", "test",
		contracts.Correlation{}, time.Now(), contracts.SMTPPayload{Outcome: "delivered"})
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

func mustJSON(t *testing.T, m map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestValidateValidEvent(t *testing.T) {
	v := newTestValidator(t)
	if err := v.ValidateRaw("smtp.delivered", mustJSON(t, baseValid(t))); err != nil {
		t.Fatalf("valid event rejected: %v", err)
	}
}

func TestValidateMissingRequiredPayloadField(t *testing.T) {
	v := newTestValidator(t)
	m := baseValid(t)
	m["payload"] = map[string]any{} // drops the required "outcome"
	if err := v.ValidateRaw("smtp.delivered", mustJSON(t, m)); err == nil {
		t.Fatal("expected validation error for missing payload.outcome")
	}
}

func TestValidateBadEnum(t *testing.T) {
	v := newTestValidator(t)
	m := baseValid(t)
	m["payload"] = map[string]any{"outcome": "teleported"} // not in enum
	if err := v.ValidateRaw("smtp.delivered", mustJSON(t, m)); err == nil {
		t.Fatal("expected validation error for bad outcome enum")
	}
}

func TestValidateAdditionalPropertyRejected(t *testing.T) {
	v := newTestValidator(t)
	m := baseValid(t)
	m["surprise"] = true // envelope is additionalProperties:false
	if err := v.ValidateRaw("smtp.delivered", mustJSON(t, m)); err == nil {
		t.Fatal("expected validation error for unexpected top-level field")
	}
}

func TestValidateUnknownEventType(t *testing.T) {
	v := newTestValidator(t)
	if err := v.ValidateRaw("telepathy.sent", mustJSON(t, baseValid(t))); err == nil {
		t.Fatal("expected error for unknown event family")
	}
}

func TestValidateDNSEvent(t *testing.T) {
	v := newTestValidator(t)
	ev, err := NewEnvelope(contracts.EventDNSValidationFailed, "tenant-1", "test",
		contracts.Correlation{Domain: "example.com"}, time.Now(),
		contracts.DNSPayload{
			Domain: "example.com", DomainID: "d", SnapshotID: "s",
			Findings: []contracts.DNSFinding{{Category: "spf", Severity: "critical", Code: "X", Message: "bad"}},
		})
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	raw, _ := json.Marshal(ev)
	if err := v.ValidateRaw(string(contracts.EventDNSValidationFailed), raw); err != nil {
		t.Fatalf("valid dns event rejected: %v", err)
	}
}
