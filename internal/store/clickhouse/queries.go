package clickhouse

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// MessageFilter holds optional predicates for QueryMessages.
type MessageFilter struct {
	TenantID  string
	Domain    string // matches from_domain OR recipient_domain
	Sender    string // matches envelope_from (exact)
	MessageID string
	Provider  string
	Outcome   string // one of: received|delivered|deferred|bounced|rejected (matches event_type)
	SASLUser  string // authenticated submission user (exact)
	Since     time.Time
	Until     time.Time
	Limit     int
	Offset    int
}

// MessageRow is one result row from QueryMessages.
type MessageRow struct {
	EventID         string
	EventTime       time.Time
	EventType       string
	Outcome         string
	MessageID       string
	QueueID         string
	FromDomain      string
	RecipientDomain string
	Provider        string
	RelayIP         string
	SMTPCode        uint16
	EnhancedStatus  string
	BounceClass     string
	ResponseText    string
	SASLUsername    string
}

// QueryMessages queries smtp_events with optional filters and returns up to Limit rows.
func (s *Store) QueryMessages(ctx context.Context, f MessageFilter) ([]MessageRow, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}

	var sb strings.Builder
	sb.WriteString(`SELECT toString(event_id), event_time, toString(event_type), message_id, queue_id, from_domain, recipient_domain, provider, toString(relay_ip), smtp_code, enhanced_status, toString(bounce_class), response_text, sasl_username FROM smtp_events WHERE tenant_id = ?`)

	args := []any{f.TenantID}

	if f.Domain != "" {
		sb.WriteString(" AND (from_domain = ? OR recipient_domain = ?)")
		args = append(args, f.Domain, f.Domain)
	}
	if f.Sender != "" {
		sb.WriteString(" AND envelope_from = ?")
		args = append(args, f.Sender)
	}
	if f.MessageID != "" {
		sb.WriteString(" AND message_id = ?")
		args = append(args, f.MessageID)
	}
	if f.Provider != "" {
		sb.WriteString(" AND provider = ?")
		args = append(args, f.Provider)
	}
	if f.Outcome != "" {
		sb.WriteString(" AND event_type = ?")
		args = append(args, f.Outcome)
	}
	if f.SASLUser != "" {
		sb.WriteString(" AND sasl_username = ?")
		args = append(args, f.SASLUser)
	}
	if !f.Since.IsZero() {
		sb.WriteString(" AND event_time >= ?")
		args = append(args, f.Since)
	}
	if !f.Until.IsZero() {
		sb.WriteString(" AND event_time <= ?")
		args = append(args, f.Until)
	}

	// event_id breaks ties so the order is a total one. event_time alone is not:
	// events sharing a timestamp could come back in any relative order, and with
	// LIMIT/OFFSET paging that means a row at a page boundary is liable to repeat on
	// the next page or be skipped entirely.
	sb.WriteString(" ORDER BY event_time DESC, event_id DESC LIMIT ?")
	args = append(args, limit)
	if f.Offset > 0 {
		sb.WriteString(" OFFSET ?")
		args = append(args, f.Offset)
	}

	rows, err := s.conn.Query(ctx, sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("query messages: %w", err)
	}
	defer rows.Close()

	var results []MessageRow
	for rows.Next() {
		var r MessageRow
		if err := rows.Scan(
			&r.EventID,
			&r.EventTime,
			&r.EventType,
			&r.MessageID,
			&r.QueueID,
			&r.FromDomain,
			&r.RecipientDomain,
			&r.Provider,
			&r.RelayIP,
			&r.SMTPCode,
			&r.EnhancedStatus,
			&r.BounceClass,
			&r.ResponseText,
			&r.SASLUsername,
		); err != nil {
			return nil, fmt.Errorf("query messages: %w", err)
		}
		r.Outcome = r.EventType
		if r.RelayIP == "::" {
			r.RelayIP = ""
		}
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query messages: %w", err)
	}
	return results, nil
}

// TraceEvent is one lifecycle event in a single message's delivery trace.
type TraceEvent struct {
	EventTime       time.Time
	EventType       string
	Provider        string
	MXHost          string
	RecipientDomain string
	SMTPCode        uint16
	EnhancedStatus  string
	BounceClass     string
	ResponseText    string
}

// MessageTrace is the full timeline for one message, keyed on the relay-local queue id.
type MessageTrace struct {
	QueueID    string
	MessageID  string
	FromDomain string
	Events     []TraceEvent // chronological (oldest first)
}

// QueryMessageTrace returns every recorded event for one message (tenant_id + queue_id),
// oldest first, for the per-message tracking page. An empty Events slice means the message
// is unknown for that tenant — callers use that as the ownership/existence check when minting
// a share link. queue_id is the stable per-message handle: a message may carry no Message-ID
// header when rejected early, but always has a relay queue id.
func (s *Store) QueryMessageTrace(ctx context.Context, tenantID, queueID string) (MessageTrace, error) {
	const q = `SELECT event_time, toString(event_type), provider, mx_host, recipient_domain,
	                  smtp_code, enhanced_status, toString(bounce_class), response_text, message_id, from_domain
	           FROM smtp_events
	           WHERE tenant_id = ? AND queue_id = ?
	           ORDER BY event_time ASC
	           LIMIT 500`

	rows, err := s.conn.Query(ctx, q, tenantID, queueID)
	if err != nil {
		return MessageTrace{}, fmt.Errorf("query message trace: %w", err)
	}
	defer rows.Close()

	trace := MessageTrace{QueueID: queueID}
	for rows.Next() {
		var (
			e          TraceEvent
			messageID  string
			fromDomain string
		)
		if err := rows.Scan(
			&e.EventTime, &e.EventType, &e.Provider, &e.MXHost, &e.RecipientDomain,
			&e.SMTPCode, &e.EnhancedStatus, &e.BounceClass, &e.ResponseText, &messageID, &fromDomain,
		); err != nil {
			return MessageTrace{}, fmt.Errorf("scan trace event: %w", err)
		}
		// Carry the first non-empty header/domain we see onto the trace summary.
		if trace.MessageID == "" && messageID != "" {
			trace.MessageID = messageID
		}
		if trace.FromDomain == "" && fromDomain != "" {
			trace.FromDomain = fromDomain
		}
		trace.Events = append(trace.Events, e)
	}
	if err := rows.Err(); err != nil {
		return MessageTrace{}, fmt.Errorf("query message trace: %w", err)
	}
	return trace, nil
}

// MessageEnvelope is the collapsed one-row summary of a message for the drill-down "Envelope"
// tab: originating IP / SASL user / envelope + auth results taken from the received event, and
// the final outcome (event type, SMTP code, response) taken from the latest event.
type MessageEnvelope struct {
	QueueID         string    `json:"queue_id"`
	MessageID       string    `json:"message_id"`
	Queued          time.Time `json:"queued"`
	LastEvent       time.Time `json:"last_event"`
	SourceIP        string    `json:"source_ip"`
	SASLUsername    string    `json:"sasl_username"`
	EnvelopeFrom    string    `json:"envelope_from"`
	FromDomain      string    `json:"from_domain"`
	RecipientDomain string    `json:"recipient_domain"`
	Provider        string    `json:"provider"`
	SPFResult       string    `json:"spf_result"`
	DKIMResult      string    `json:"dkim_result"`
	DMARCResult     string    `json:"dmarc_result"`
	TLSUsed         uint8     `json:"tls_used"`
	TLSVersion      string    `json:"tls_version"`
	SizeBytes       uint32    `json:"size_bytes"`
	Outcome         string    `json:"outcome"`
	SMTPCode        uint16    `json:"smtp_code"`
	EnhancedStatus  string    `json:"enhanced_status"`
	ResponseText    string    `json:"response_text"`
}

// QueryMessageEnvelope collapses all smtp_events for one message (tenant_id + queue_id) into a
// single envelope summary. ok=false means the message is unknown for that tenant.
func (s *Store) QueryMessageEnvelope(ctx context.Context, tenantID, queueID string) (MessageEnvelope, bool, error) {
	const q = `SELECT
		min(event_time)                                                  AS queued,
		max(event_time)                                                  AS last_event,
		anyIf(toString(source_ip), event_type = 'received')              AS source_ip,
		anyIf(sasl_username, sasl_username != '')                        AS sasl_username,
		anyIf(envelope_from, envelope_from != '')                        AS envelope_from,
		anyIf(from_domain, from_domain != '')                            AS from_domain,
		anyIf(recipient_domain, recipient_domain != '')                  AS recipient_domain,
		anyIf(provider, provider != '')                                  AS provider,
		anyIf(toString(spf_result), event_type = 'received')             AS spf_result,
		anyIf(toString(dkim_result), event_type = 'received')            AS dkim_result,
		anyIf(toString(dmarc_result), event_type = 'received')           AS dmarc_result,
		max(tls_used)                                                    AS tls_used,
		anyIf(tls_version, tls_version != '')                            AS tls_version,
		max(size_bytes)                                                  AS size_bytes,
		anyIf(message_id, message_id != '')                              AS message_id,
		argMax(toString(event_type), event_time)                         AS outcome,
		argMax(smtp_code, event_time)                                    AS smtp_code,
		argMax(enhanced_status, event_time)                              AS enhanced_status,
		argMax(response_text, event_time)                                AS response_text
	FROM smtp_events
	WHERE tenant_id = ? AND queue_id = ?
	HAVING count() > 0`

	row := s.conn.QueryRow(ctx, q, tenantID, queueID)
	var e MessageEnvelope
	e.QueueID = queueID
	if err := row.Scan(
		&e.Queued, &e.LastEvent, &e.SourceIP, &e.SASLUsername, &e.EnvelopeFrom, &e.FromDomain,
		&e.RecipientDomain, &e.Provider, &e.SPFResult, &e.DKIMResult, &e.DMARCResult,
		&e.TLSUsed, &e.TLSVersion, &e.SizeBytes, &e.MessageID,
		&e.Outcome, &e.SMTPCode, &e.EnhancedStatus, &e.ResponseText,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return MessageEnvelope{}, false, nil
		}
		return MessageEnvelope{}, false, fmt.Errorf("query message envelope: %w", err)
	}
	return e, true, nil
}

// DMARCAlignment holds aggregate alignment counts for a domain.
type DMARCAlignment struct {
	Total       uint64
	DKIMAligned uint64
	SPFAligned  uint64
}

// DMARCAlignmentSummary returns aggregate DKIM/SPF alignment counts from dmarc_records.
func (s *Store) DMARCAlignmentSummary(ctx context.Context, tenantID, domain string, since, until time.Time) (DMARCAlignment, error) {
	var sb strings.Builder
	sb.WriteString(`SELECT sum(count), sum(count * dkim_aligned), sum(count * spf_aligned) FROM dmarc_records WHERE tenant_id = ?`)

	args := []any{tenantID}

	if domain != "" {
		sb.WriteString(" AND domain = ?")
		args = append(args, domain)
	}
	if !since.IsZero() {
		sb.WriteString(" AND date_begin >= ?")
		args = append(args, since)
	}
	if !until.IsZero() {
		sb.WriteString(" AND date_begin <= ?")
		args = append(args, until)
	}

	var a DMARCAlignment
	if err := s.conn.QueryRow(ctx, sb.String(), args...).Scan(&a.Total, &a.DKIMAligned, &a.SPFAligned); err != nil {
		return DMARCAlignment{}, fmt.Errorf("dmarc alignment summary: %w", err)
	}
	return a, nil
}
