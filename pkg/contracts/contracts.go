// Package contracts holds the Go types for MX Sentinel's event bus messages. These
// mirror the JSON Schemas under schemas/events (the source of truth) and are imported by
// every producer and consumer. Keep the json tags in lock-step with the schemas — a
// drift between this file and schemas/events is a bug (see docs/event-contracts.md).
//
// This package depends only on the standard library so anything can import it.
package contracts

import (
	"encoding/json"
	"strings"
)

// SchemaVersion is the semver of the payload schemas these types implement.
const SchemaVersion = "1.0.0"

// EventType is the dotted event type carried in the envelope.
type EventType string

const (
	// SMTP telemetry.
	EventSMTPReceived  EventType = "smtp.received"
	EventSMTPDelivered EventType = "smtp.delivered"
	EventSMTPDeferred  EventType = "smtp.deferred"
	EventSMTPBounced   EventType = "smtp.bounced"
	EventSMTPRejected  EventType = "smtp.rejected"

	// DNS intelligence.
	EventDNSChanged          EventType = "dns.changed"
	EventDNSValidationFailed EventType = "dns.validation_failed"

	// Reputation signals.
	EventReputationBlacklistHit   EventType = "reputation.blacklist_hit"
	EventReputationComplaintSpike EventType = "reputation.complaint_spike"
	EventReputationRateAnomaly    EventType = "reputation.rate_anomaly"

	// AI reasoning.
	EventAIAnomaly     EventType = "ai.anomaly"
	EventAIRemediation EventType = "ai.remediation"
	EventAIRCA         EventType = "ai.rca"
)

// Family returns the leading segment of an event type ("smtp", "dns", "reputation",
// "ai"), or "" if malformed.
func (t EventType) Family() string {
	if i := strings.IndexByte(string(t), '.'); i > 0 {
		return string(t)[:i]
	}
	return ""
}

// Correlation carries the join keys lifted out of the payload so consumers can correlate
// without parsing payloads they don't understand. All fields optional.
type Correlation struct {
	MessageID    string `json:"message_id,omitempty"`
	QueueID      string `json:"queue_id,omitempty"`
	SessionID    string `json:"session_id,omitempty"`
	SourceIP     string `json:"source_ip,omitempty"`
	RelayIP      string `json:"relay_ip,omitempty"`
	DKIMSelector string `json:"dkim_selector,omitempty"`
	EnvelopeFrom string `json:"envelope_from,omitempty"`
	Domain       string `json:"domain,omitempty"`
}

// Envelope is the common wrapper for every bus message. Payload holds the type-specific
// body as raw JSON so the envelope can be decoded without knowing the payload shape.
type Envelope struct {
	SchemaVersion string          `json:"schema_version"`
	EventID       string          `json:"event_id"`
	EventType     EventType       `json:"event_type"`
	TenantID      string          `json:"tenant_id"`
	OccurredAt    string          `json:"occurred_at"`
	IngestedAt    string          `json:"ingested_at"`
	Source        string          `json:"source"`
	Correlation   Correlation     `json:"correlation"`
	Payload       json.RawMessage `json:"payload"`
}

// DecodePayload unmarshals the raw payload into v.
func (e *Envelope) DecodePayload(v any) error { return json.Unmarshal(e.Payload, v) }

// ----------------------------------------------------------------------------
// Payloads (mirror schemas/events/*.json)
// ----------------------------------------------------------------------------

// TLSInfo is the TLS metadata for an SMTP transaction.
type TLSInfo struct {
	Used     bool   `json:"used,omitempty"`
	Version  string `json:"version,omitempty"`
	Cipher   string `json:"cipher,omitempty"`
	Verified bool   `json:"verified,omitempty"`
}

// SMTPPayload is the body of an smtp.* event. NO message body or subject — see the
// privacy boundary in docs/data-model.md.
type SMTPPayload struct {
	Outcome     string `json:"outcome"`
	BounceClass string `json:"bounce_class,omitempty"`

	MessageID    string `json:"message_id,omitempty"`
	QueueID      string `json:"queue_id,omitempty"`
	SessionID    string `json:"session_id,omitempty"`
	SourceIP     string `json:"source_ip,omitempty"`
	RelayIP      string `json:"relay_ip,omitempty"`
	IPPool       string `json:"ip_pool,omitempty"`
	DKIMSelector string `json:"dkim_selector,omitempty"`
	DKIMDomain   string `json:"dkim_domain,omitempty"`
	EnvelopeFrom string `json:"envelope_from,omitempty"`
	FromDomain   string `json:"from_domain,omitempty"`

	RecipientDomain string `json:"recipient_domain,omitempty"`
	RecipientHash   string `json:"recipient_hash,omitempty"`

	Provider string `json:"provider,omitempty"`
	MXHost   string `json:"mx_host,omitempty"`

	SPFResult   string `json:"spf_result,omitempty"`
	DKIMResult  string `json:"dkim_result,omitempty"`
	DMARCResult string `json:"dmarc_result,omitempty"`

	TLS *TLSInfo `json:"tls,omitempty"`

	SMTPCode       int    `json:"smtp_code,omitempty"`
	EnhancedStatus string `json:"enhanced_status,omitempty"`
	ResponseText   string `json:"response_text,omitempty"`

	// SASLUsername is the authenticated submission user (SASL login) the relay accepted
	// the message from. Set for outbound/authenticated mail; enables per-account abuse
	// attribution and auto-suspension (cmd/abused).
	SASLUsername string `json:"sasl_username,omitempty"`

	SizeBytes         int `json:"size_bytes,omitempty"`
	QueueTimeMS       int `json:"queue_time_ms,omitempty"`
	DeliveryLatencyMS int `json:"delivery_latency_ms,omitempty"`

	Attrs map[string]string `json:"attrs,omitempty"`
}

// DNSFinding is a single validation result attached to a DNS snapshot.
type DNSFinding struct {
	Category string         `json:"category"`
	Severity string         `json:"severity"`
	Code     string         `json:"code"`
	Message  string         `json:"message"`
	Detail   map[string]any `json:"detail,omitempty"`
}

// DNSPayload is the body of a dns.* event.
type DNSPayload struct {
	Domain             string       `json:"domain"`
	DomainID           string       `json:"domain_id"`
	SnapshotID         string       `json:"snapshot_id"`
	PreviousSnapshotID string       `json:"previous_snapshot_id,omitempty"`
	Checksum           string       `json:"checksum,omitempty"`
	ChangedCategories  []string     `json:"changed_categories,omitempty"`
	Findings           []DNSFinding `json:"findings,omitempty"`
}

// ReputationPayload is the body of a reputation.* event.
type ReputationPayload struct {
	Signal        string         `json:"signal"`
	SubjectKind   string         `json:"subject_kind"`
	Subject       string         `json:"subject"`
	Severity      string         `json:"severity"`
	Source        string         `json:"source,omitempty"`
	ObservedValue float64        `json:"observed_value,omitempty"`
	BaselineValue float64        `json:"baseline_value,omitempty"`
	WindowSecs    int            `json:"window_secs,omitempty"`
	Detail        map[string]any `json:"detail,omitempty"`
}

// AIImpact describes the blast radius of an AI conclusion.
type AIImpact struct {
	AffectedMessages int    `json:"affected_messages,omitempty"`
	Provider         string `json:"provider,omitempty"`
	Domain           string `json:"domain,omitempty"`
}

// AIRecommendation is a single machine-actionable suggestion.
type AIRecommendation struct {
	Action   string `json:"action"`
	Summary  string `json:"summary"`
	Target   string `json:"target,omitempty"`
	Priority string `json:"priority,omitempty"`
}

// AIPayload is the body of an ai.* event.
type AIPayload struct {
	Kind             string             `json:"kind"`
	Title            string             `json:"title"`
	Narrative        string             `json:"narrative"`
	Confidence       float64            `json:"confidence"`
	Impact           *AIImpact          `json:"impact,omitempty"`
	Recommendations  []AIRecommendation `json:"recommendations,omitempty"`
	EvidenceEventIDs []string           `json:"evidence_event_ids,omitempty"`
	Model            string             `json:"model,omitempty"`
}
