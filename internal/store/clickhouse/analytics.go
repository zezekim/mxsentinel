package clickhouse

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ProviderStats holds per-provider outcome counts for a tenant.
type ProviderStats struct {
	Provider  string
	Delivered uint64
	Deferred  uint64
	Bounced   uint64
	Rejected  uint64
	Total     uint64
}

// DeliverabilityByProvider returns per-provider outcome counts for a tenant in a time
// window (since/until are optional; zero value means unbounded on that side).
func (s *Store) DeliverabilityByProvider(ctx context.Context, tenantID string, since, until time.Time) ([]ProviderStats, error) {
	var sb strings.Builder
	sb.WriteString(`SELECT provider, countIf(event_type = 'delivered'), countIf(event_type = 'deferred'), countIf(event_type = 'bounced'), countIf(event_type = 'rejected'), count() FROM smtp_events WHERE tenant_id = ?`)

	args := []any{tenantID}

	if !since.IsZero() {
		sb.WriteString(" AND event_time >= ?")
		args = append(args, since)
	}
	if !until.IsZero() {
		sb.WriteString(" AND event_time <= ?")
		args = append(args, until)
	}

	sb.WriteString(" GROUP BY provider ORDER BY count() DESC")

	rows, err := s.conn.Query(ctx, sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("deliverability by provider: %w", err)
	}
	defer rows.Close()

	var results []ProviderStats
	for rows.Next() {
		var p ProviderStats
		if err := rows.Scan(
			&p.Provider,
			&p.Delivered,
			&p.Deferred,
			&p.Bounced,
			&p.Rejected,
			&p.Total,
		); err != nil {
			return nil, fmt.Errorf("deliverability by provider: %w", err)
		}
		results = append(results, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("deliverability by provider: %w", err)
	}
	return results, nil
}

// RejectionGroup holds a group of rejected/bounced events sharing the same
// (smtp_code, enhanced_status, bounce_class, provider) tuple.
type RejectionGroup struct {
	SMTPCode       uint16
	EnhancedStatus string
	BounceClass    string
	Provider       string
	Sample         string // a representative response_text for this group
	Count          uint64
}

// RejectionGroups returns rejected+bounced events grouped by (smtp_code, enhanced_status,
// bounce_class, provider) with a sample response and count, most frequent first.
func (s *Store) RejectionGroups(ctx context.Context, tenantID string, since, until time.Time, limit int) ([]RejectionGroup, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}

	var sb strings.Builder
	sb.WriteString(`SELECT smtp_code, enhanced_status, toString(bounce_class), provider, any(response_text), count() FROM smtp_events WHERE tenant_id = ? AND event_type IN ('bounced','rejected')`)

	args := []any{tenantID}

	if !since.IsZero() {
		sb.WriteString(" AND event_time >= ?")
		args = append(args, since)
	}
	if !until.IsZero() {
		sb.WriteString(" AND event_time <= ?")
		args = append(args, until)
	}

	sb.WriteString(" GROUP BY smtp_code, enhanced_status, bounce_class, provider ORDER BY count() DESC LIMIT ?")
	args = append(args, limit)

	rows, err := s.conn.Query(ctx, sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("rejection groups: %w", err)
	}
	defer rows.Close()

	var results []RejectionGroup
	for rows.Next() {
		var g RejectionGroup
		if err := rows.Scan(
			&g.SMTPCode,
			&g.EnhancedStatus,
			&g.BounceClass,
			&g.Provider,
			&g.Sample,
			&g.Count,
		); err != nil {
			return nil, fmt.Errorf("rejection groups: %w", err)
		}
		results = append(results, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rejection groups: %w", err)
	}
	return results, nil
}
