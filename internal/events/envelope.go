package events

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/zezekim/mxsentinel/pkg/contracts"
)

// NewEnvelope constructs an event envelope: it allocates a time-sortable UUIDv7
// event_id, stamps occurred_at/ingested_at, and marshals the typed payload.
//
// occurredAt is when the real-world event happened (relay timestamp, DNS poll time);
// ingested_at is stamped now. Pass the payload as one of the contracts.*Payload types.
func NewEnvelope(
	t contracts.EventType,
	tenantID, source string,
	corr contracts.Correlation,
	occurredAt time.Time,
	payload any,
) (*contracts.Envelope, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("generate event id: %w", err)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}
	now := time.Now().UTC()
	return &contracts.Envelope{
		SchemaVersion: contracts.SchemaVersion,
		EventID:       id.String(),
		EventType:     t,
		TenantID:      tenantID,
		OccurredAt:    occurredAt.UTC().Format(time.RFC3339Nano),
		IngestedAt:    now.Format(time.RFC3339Nano),
		Source:        source,
		Correlation:   corr,
		Payload:       raw,
	}, nil
}
