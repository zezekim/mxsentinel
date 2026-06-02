package incidents

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/zezekim/mxsentinel/internal/events"
	"github.com/zezekim/mxsentinel/pkg/contracts"
)

func envelope(t *testing.T, typ contracts.EventType, corr contracts.Correlation, payload any) *contracts.Envelope {
	t.Helper()
	ev, err := events.NewEnvelope(typ, "tenant-1", "test", corr, time.Now(), payload)
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	return ev
}

func TestFromRateAnomaly(t *testing.T) {
	ev := envelope(t, contracts.EventReputationRateAnomaly, contracts.Correlation{Domain: "example.com"},
		contracts.ReputationPayload{
			Signal: "rate_anomaly", SubjectKind: "domain", Subject: "example.com",
			Severity: "critical", Source: "microsoft",
			Detail: map[string]any{"root_cause": "DKIM selector missing after DNS change", "confidence": 0.85},
		})
	in, ok := FromEnvelope(ev)
	if !ok {
		t.Fatal("expected ok")
	}
	if in.Kind != "rejection_spike" || in.Severity != "critical" || in.Domain != "example.com" {
		t.Errorf("bad mapping: %+v", in)
	}
	if in.Title != "DKIM selector missing after DNS change" {
		t.Errorf("title = %q", in.Title)
	}
	if in.Confidence == nil || *in.Confidence != 0.85 {
		t.Errorf("confidence = %v, want 0.85", in.Confidence)
	}
	if in.SourceEventID != ev.EventID {
		t.Errorf("source_event_id not carried")
	}
}

func TestFromBlacklistHit(t *testing.T) {
	ev := envelope(t, contracts.EventReputationBlacklistHit, contracts.Correlation{},
		contracts.ReputationPayload{
			Signal: "blacklist_hit", SubjectKind: "ip", Subject: "198.51.100.5",
			Severity: "critical", Source: "zen.spamhaus.org",
		})
	in, ok := FromEnvelope(ev)
	if !ok {
		t.Fatal("expected ok")
	}
	if in.Kind != "blacklist" || in.Subject != "198.51.100.5" {
		t.Errorf("bad mapping: %+v", in)
	}
	if !strings.Contains(in.Title, "Blacklist hit") || !strings.Contains(in.Title, "zen.spamhaus.org") {
		t.Errorf("title = %q", in.Title)
	}
}

func TestFromDNSValidationFailed(t *testing.T) {
	ev := envelope(t, contracts.EventDNSValidationFailed, contracts.Correlation{Domain: "example.com"},
		contracts.DNSPayload{
			Domain: "example.com", DomainID: "d1", SnapshotID: "s1",
			Findings: []contracts.DNSFinding{
				{Category: "spf", Severity: "warning", Code: "SPF_MISSING", Message: "x"},
				{Category: "dkim", Severity: "critical", Code: "DKIM_MISSING_SELECTOR", Message: "y"},
			},
		})
	in, ok := FromEnvelope(ev)
	if !ok {
		t.Fatal("expected ok")
	}
	if in.Kind != "dns_validation" || in.Domain != "example.com" || in.Severity != "critical" {
		t.Errorf("bad mapping: %+v", in)
	}
	var d map[string]any
	if err := json.Unmarshal(in.Detail, &d); err != nil {
		t.Fatalf("detail not json: %v", err)
	}
	if !strings.Contains(string(in.Detail), "DKIM_MISSING_SELECTOR") {
		t.Errorf("detail missing codes: %s", in.Detail)
	}
}

func TestFromUnrelatedEvent(t *testing.T) {
	ev := envelope(t, contracts.EventSMTPDelivered, contracts.Correlation{},
		contracts.SMTPPayload{Outcome: "delivered"})
	if _, ok := FromEnvelope(ev); ok {
		t.Error("smtp.delivered should not map to an incident")
	}
}
