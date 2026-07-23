package clickhouse

import (
	"context"
	"fmt"
	"time"
)

// BounceRow is one bounced/rejected event pulled from smtp_events for classification. It
// carries only the fields the classifier and suppression need — recipient_hash (the keyed
// hash, never a plaintext address), the codes, and the diagnostic text.
type BounceRow struct {
	TenantID        string
	EventTime       time.Time
	FromDomain      string
	RecipientDomain string
	RecipientHash   string
	Provider        string
	SMTPCode        uint16
	EnhancedStatus  string
	ResponseText    string
}

// RecentBounces returns a tenant's bounced+rejected events since a given time, newest
// first, up to limit rows. Used by the classified-bounce feed (GET /v1/bounces).
func (s *Store) RecentBounces(ctx context.Context, tenantID string, since time.Time, limit int) ([]BounceRow, error) {
	if limit <= 0 || limit > 5000 {
		limit = 500
	}
	const q = `
		SELECT tenant_id, event_time, from_domain, recipient_domain, recipient_hash,
		       provider, smtp_code, enhanced_status, response_text
		FROM smtp_events
		WHERE tenant_id = ? AND event_type IN ('bounced','rejected') AND event_time >= ?
		ORDER BY event_time DESC
		LIMIT ?`
	rows, err := s.conn.Query(ctx, q, tenantID, since, limit)
	if err != nil {
		return nil, fmt.Errorf("recent bounces: %w", err)
	}
	defer rows.Close()
	return scanBounceRows(rows)
}

// RecentBouncesAllTenants returns bounced+rejected events across ALL tenants since a given
// time, newest first, up to limit rows. The bounced daemon uses this single query to drive
// both the classified rollup and suppression updates without needing a tenant list.
func (s *Store) RecentBouncesAllTenants(ctx context.Context, since time.Time, limit int) ([]BounceRow, error) {
	if limit <= 0 {
		limit = 100000
	}
	const q = `
		SELECT tenant_id, event_time, from_domain, recipient_domain, recipient_hash,
		       provider, smtp_code, enhanced_status, response_text
		FROM smtp_events
		WHERE event_type IN ('bounced','rejected') AND event_time >= ?
		ORDER BY event_time DESC
		LIMIT ?`
	rows, err := s.conn.Query(ctx, q, since, limit)
	if err != nil {
		return nil, fmt.Errorf("recent bounces (all tenants): %w", err)
	}
	defer rows.Close()
	return scanBounceRows(rows)
}

func scanBounceRows(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
}) ([]BounceRow, error) {
	var out []BounceRow
	for rows.Next() {
		var b BounceRow
		if err := rows.Scan(
			&b.TenantID, &b.EventTime, &b.FromDomain, &b.RecipientDomain, &b.RecipientHash,
			&b.Provider, &b.SMTPCode, &b.EnhancedStatus, &b.ResponseText,
		); err != nil {
			return nil, fmt.Errorf("scan bounce row: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// DomainBounceRate is a per-sending-domain bounce-rate summary over a window.
type DomainBounceRate struct {
	Domain  string
	Total   uint64  // all delivery attempts (any event_type)
	Bounced uint64  // bounced + rejected
	Rate    float64 // Bounced / Total, 0 when Total == 0
}

// DomainBounceRates returns per-domain bounce rates for a tenant since a given day, read
// from the bounce_daily rollup (migrations/clickhouse/00005_bounce_events.sql). Ordered by
// bounce volume descending, capped at limit domains.
func (s *Store) DomainBounceRates(ctx context.Context, tenantID string, since time.Time, limit int) ([]DomainBounceRate, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	const q = `
		SELECT from_domain,
		       sum(sent)               AS total,
		       sum(bounced + rejected) AS bounced
		FROM bounce_daily
		WHERE tenant_id = ? AND day >= ?
		GROUP BY from_domain
		HAVING total > 0
		ORDER BY bounced DESC, total DESC
		LIMIT ?`
	rows, err := s.conn.Query(ctx, q, tenantID, since, limit)
	if err != nil {
		return nil, fmt.Errorf("domain bounce rates: %w", err)
	}
	defer rows.Close()

	var out []DomainBounceRate
	for rows.Next() {
		var d DomainBounceRate
		if err := rows.Scan(&d.Domain, &d.Total, &d.Bounced); err != nil {
			return nil, fmt.Errorf("scan domain bounce rate: %w", err)
		}
		if d.Total > 0 {
			d.Rate = float64(d.Bounced) / float64(d.Total)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
