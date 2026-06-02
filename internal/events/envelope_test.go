package events

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zezekim/mxsentinel/pkg/contracts"
)

func TestNewEnvelope(t *testing.T) {
	occurred := time.Date(2026, 6, 2, 10, 45, 0, 0, time.UTC)
	ev, err := NewEnvelope(
		contracts.EventSMTPDelivered, "tenant-1", "test",
		contracts.Correlation{MessageID: "<m@example.com>"},
		occurred,
		contracts.SMTPPayload{Outcome: "delivered", Provider: "gmail"},
	)
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}

	if ev.SchemaVersion != contracts.SchemaVersion {
		t.Errorf("schema_version = %q", ev.SchemaVersion)
	}
	if ev.EventType != contracts.EventSMTPDelivered {
		t.Errorf("event_type = %q", ev.EventType)
	}
	if _, err := uuid.Parse(ev.EventID); err != nil {
		t.Errorf("event_id not a uuid: %v", err)
	}
	if ev.OccurredAt != occurred.Format(time.RFC3339Nano) {
		t.Errorf("occurred_at = %q", ev.OccurredAt)
	}
	if ev.Correlation.MessageID != "<m@example.com>" {
		t.Errorf("correlation lost: %+v", ev.Correlation)
	}

	var p contracts.SMTPPayload
	if err := ev.DecodePayload(&p); err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if p.Outcome != "delivered" || p.Provider != "gmail" {
		t.Errorf("payload round-trip lost data: %+v", p)
	}
}
