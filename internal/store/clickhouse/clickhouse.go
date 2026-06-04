// Package clickhouse is the telemetry access layer. It writes SMTP events in batches to
// the mxsentinel.smtp_events table (schemas/clickhouse/001_smtp_telemetry.sql) and is the
// read path for deliverability analytics. This is the high-ingest store.
package clickhouse

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/zezekim/mxsentinel/internal/config"
)

// Store wraps a ClickHouse connection.
type Store struct {
	conn driver.Conn
}

// New opens a ClickHouse connection and verifies connectivity.
func New(ctx context.Context, cfg config.ClickHouseConfig) (*Store, error) {
	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr: []string{cfg.Addr},
		Auth: clickhouse.Auth{
			Database: cfg.Database,
			Username: cfg.Username,
			Password: cfg.Password,
		},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("open clickhouse: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := conn.Ping(pingCtx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("ping clickhouse: %w", err)
	}
	return &Store{conn: conn}, nil
}

// Ping verifies connectivity.
func (s *Store) Ping(ctx context.Context) error { return s.conn.Ping(ctx) }

// Close closes the connection.
func (s *Store) Close() error { return s.conn.Close() }

// SMTPEventRow is one row for mxsentinel.smtp_events. ingestd builds these from
// contracts.Envelope + SMTPPayload. Enum columns take their string label; IP columns take
// net.IP. Zero values are fine — ClickHouse applies column defaults for anything omitted.
type SMTPEventRow struct {
	EventID    string    // UUID (string accepted)
	EventTime  time.Time // occurred_at
	IngestedAt time.Time
	TenantID   string
	Source     string

	EventType   string // delivered | deferred | bounced | rejected | received
	BounceClass string

	MessageID    string
	QueueID      string
	SessionID    string
	SourceIP     net.IP
	RelayIP      net.IP
	IPPool       string
	DKIMSelector string
	DKIMDomain   string
	EnvelopeFrom string
	FromDomain   string

	RecipientDomain string
	RecipientHash   string

	Provider string
	MXHost   string

	SPFResult   string
	DKIMResult  string
	DMARCResult string

	TLSUsed     uint8
	TLSVersion  string
	TLSCipher   string
	TLSVerified uint8

	SMTPCode       uint16
	EnhancedStatus string
	ResponseText   string
	SASLUsername   string

	SizeBytes         uint32
	QueueTimeMS       uint32
	DeliveryLatencyMS uint32

	Attrs map[string]string
}

// smtpInsertColumns is the explicit column list for batch inserts; columns not listed
// take ClickHouse defaults. Order MUST match the Append call in InsertSMTPEvents.
const smtpInsertStmt = `INSERT INTO mxsentinel.smtp_events (
	event_id, event_time, ingested_at, tenant_id, source,
	event_type, bounce_class,
	message_id, queue_id, session_id, source_ip, relay_ip, ip_pool,
	dkim_selector, dkim_domain, envelope_from, from_domain,
	recipient_domain, recipient_hash, provider, mx_host,
	spf_result, dkim_result, dmarc_result,
	tls_used, tls_version, tls_cipher, tls_verified,
	smtp_code, enhanced_status, response_text, sasl_username,
	size_bytes, queue_time_ms, delivery_latency_ms, attrs
)`

// InsertSMTPEvents writes a batch of telemetry rows.
func (s *Store) InsertSMTPEvents(ctx context.Context, rows []SMTPEventRow) error {
	if len(rows) == 0 {
		return nil
	}
	batch, err := s.conn.PrepareBatch(ctx, smtpInsertStmt)
	if err != nil {
		return fmt.Errorf("prepare smtp_events batch: %w", err)
	}
	for i := range rows {
		r := &rows[i]
		attrs := r.Attrs
		if attrs == nil {
			attrs = map[string]string{}
		}
		if err := batch.Append(
			r.EventID, r.EventTime, r.IngestedAt, r.TenantID, r.Source,
			r.EventType, r.BounceClass,
			r.MessageID, r.QueueID, r.SessionID, ipOrZero(r.SourceIP), ipOrZero(r.RelayIP), r.IPPool,
			r.DKIMSelector, r.DKIMDomain, r.EnvelopeFrom, r.FromDomain,
			r.RecipientDomain, r.RecipientHash, r.Provider, r.MXHost,
			r.SPFResult, r.DKIMResult, r.DMARCResult,
			r.TLSUsed, r.TLSVersion, r.TLSCipher, r.TLSVerified,
			r.SMTPCode, r.EnhancedStatus, r.ResponseText, r.SASLUsername,
			r.SizeBytes, r.QueueTimeMS, r.DeliveryLatencyMS, attrs,
		); err != nil {
			return fmt.Errorf("append row %d: %w", i, err)
		}
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("send smtp_events batch: %w", err)
	}
	return nil
}

// ipOrZero returns a non-nil IP for ClickHouse (unspecified IPv6 when absent).
func ipOrZero(ip net.IP) net.IP {
	if ip == nil {
		return net.IPv6zero
	}
	return ip
}
