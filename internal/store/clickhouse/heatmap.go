package clickhouse

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ProviderHeatmapRow holds per-(provider, recipient_domain) outcome counts.
type ProviderHeatmapRow struct {
	Provider        string
	RecipientDomain string
	Delivered       uint64
	Deferred        uint64
	Bounced         uint64
	Rejected        uint64
	Total           uint64
	AcceptanceRate  float64 // delivered / total if total > 0, else 0
}

// DomainVolumeRow holds per-from_domain outcome counts.
type DomainVolumeRow struct {
	FromDomain string
	Delivered  uint64
	Bounced    uint64
	Rejected   uint64
	Total      uint64
}

// PerUserStat holds per-sasl_username stats for a time window.
type PerUserStat struct {
	SASLUsername     string
	Sent             uint64
	Delivered        uint64
	Bounced          uint64
	Rejected         uint64
	UniqueRecipients uint64
	BounceRate       float64
}

// appendTimeFilters adds since/until WHERE clauses to the builder and appends
// the corresponding args. It assumes a WHERE clause is already open (i.e. the
// caller has already written "WHERE ..."). Each filter is appended with AND.
func appendTimeFilters(b *strings.Builder, args []any, since, until time.Time) []any {
	if !since.IsZero() {
		b.WriteString(" AND event_time >= ?")
		args = append(args, since)
	}
	if !until.IsZero() {
		b.WriteString(" AND event_time <= ?")
		args = append(args, until)
	}
	return args
}

// ProviderHeatmap returns per-(provider, recipient_domain) outcome breakdown.
// since and until are optional (zero value means unbounded).
// Results are ordered by total volume DESC, limited to 100 rows.
func (s *Store) ProviderHeatmap(ctx context.Context, tenantID string, since, until time.Time) ([]ProviderHeatmapRow, error) {
	var b strings.Builder
	args := []any{tenantID}

	b.WriteString(`
SELECT
    provider,
    recipient_domain,
    countIf(event_type = 'delivered') AS delivered,
    countIf(event_type = 'deferred')  AS deferred,
    countIf(event_type = 'bounced')   AS bounced,
    countIf(event_type = 'rejected')  AS rejected,
    count()                           AS total
FROM mxsentinel.smtp_events
WHERE tenant_id = ?`)

	args = appendTimeFilters(&b, args, since, until)
	b.WriteString(`
GROUP BY provider, recipient_domain
ORDER BY total DESC
LIMIT 100`)

	rows, err := s.conn.Query(ctx, b.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("provider heatmap query: %w", err)
	}
	defer rows.Close()

	var result []ProviderHeatmapRow
	for rows.Next() {
		var r ProviderHeatmapRow
		if err := rows.Scan(
			&r.Provider,
			&r.RecipientDomain,
			&r.Delivered,
			&r.Deferred,
			&r.Bounced,
			&r.Rejected,
			&r.Total,
		); err != nil {
			return nil, fmt.Errorf("provider heatmap scan: %w", err)
		}
		if r.Total > 0 {
			r.AcceptanceRate = float64(r.Delivered) / float64(r.Total)
		}
		result = append(result, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("provider heatmap rows: %w", err)
	}
	return result, nil
}

// TopRecipientDomains returns the top N recipient domains by total volume.
// n defaults to 20 if <= 0 and is capped at 50.
func (s *Store) TopRecipientDomains(ctx context.Context, tenantID string, since, until time.Time, n int) ([]ProviderHeatmapRow, error) {
	if n <= 0 {
		n = 20
	}
	if n > 50 {
		n = 50
	}

	var b strings.Builder
	args := []any{tenantID}

	b.WriteString(`
SELECT
    provider,
    recipient_domain,
    countIf(event_type = 'delivered') AS delivered,
    countIf(event_type = 'deferred')  AS deferred,
    countIf(event_type = 'bounced')   AS bounced,
    countIf(event_type = 'rejected')  AS rejected,
    count()                           AS total
FROM mxsentinel.smtp_events
WHERE tenant_id = ?`)

	args = appendTimeFilters(&b, args, since, until)
	b.WriteString(`
GROUP BY provider, recipient_domain
ORDER BY total DESC
LIMIT ?`)
	args = append(args, uint64(n))

	rows, err := s.conn.Query(ctx, b.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("top recipient domains query: %w", err)
	}
	defer rows.Close()

	var result []ProviderHeatmapRow
	for rows.Next() {
		var r ProviderHeatmapRow
		if err := rows.Scan(
			&r.Provider,
			&r.RecipientDomain,
			&r.Delivered,
			&r.Deferred,
			&r.Bounced,
			&r.Rejected,
			&r.Total,
		); err != nil {
			return nil, fmt.Errorf("top recipient domains scan: %w", err)
		}
		if r.Total > 0 {
			r.AcceptanceRate = float64(r.Delivered) / float64(r.Total)
		}
		result = append(result, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("top recipient domains rows: %w", err)
	}
	return result, nil
}

// FromDomainVolume returns per-from_domain volume for a tenant.
// Used for the warm-up advisor. Results are ordered by total DESC, limited to 50.
func (s *Store) FromDomainVolume(ctx context.Context, tenantID string, since, until time.Time) ([]DomainVolumeRow, error) {
	var b strings.Builder
	args := []any{tenantID}

	b.WriteString(`
SELECT
    from_domain,
    countIf(event_type = 'delivered') AS delivered,
    countIf(event_type = 'bounced')   AS bounced,
    countIf(event_type = 'rejected')  AS rejected,
    count()                           AS total
FROM mxsentinel.smtp_events
WHERE tenant_id = ?`)

	args = appendTimeFilters(&b, args, since, until)
	b.WriteString(`
GROUP BY from_domain
ORDER BY total DESC
LIMIT 50`)

	rows, err := s.conn.Query(ctx, b.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("from domain volume query: %w", err)
	}
	defer rows.Close()

	var result []DomainVolumeRow
	for rows.Next() {
		var r DomainVolumeRow
		if err := rows.Scan(
			&r.FromDomain,
			&r.Delivered,
			&r.Bounced,
			&r.Rejected,
			&r.Total,
		); err != nil {
			return nil, fmt.Errorf("from domain volume scan: %w", err)
		}
		result = append(result, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("from domain volume rows: %w", err)
	}
	return result, nil
}

// PerUserStats returns per-sasl_username stats for a time window.
// Used for the SMTP user abuse dashboard.
func (s *Store) PerUserStats(ctx context.Context, tenantID string, since, until time.Time) ([]PerUserStat, error) {
	var b strings.Builder
	args := []any{tenantID}

	b.WriteString(`
SELECT
    sasl_username,
    count()                            AS sent,
    countIf(event_type = 'delivered') AS delivered,
    countIf(event_type = 'bounced')   AS bounced,
    countIf(event_type = 'rejected')  AS rejected,
    uniq(recipient_hash)              AS unique_recipients
FROM mxsentinel.smtp_events
WHERE tenant_id = ?`)

	args = appendTimeFilters(&b, args, since, until)
	b.WriteString(`
GROUP BY sasl_username
ORDER BY sent DESC`)

	rows, err := s.conn.Query(ctx, b.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("per user stats query: %w", err)
	}
	defer rows.Close()

	var result []PerUserStat
	for rows.Next() {
		var r PerUserStat
		if err := rows.Scan(
			&r.SASLUsername,
			&r.Sent,
			&r.Delivered,
			&r.Bounced,
			&r.Rejected,
			&r.UniqueRecipients,
		); err != nil {
			return nil, fmt.Errorf("per user stats scan: %w", err)
		}
		if r.Sent > 0 {
			r.BounceRate = float64(r.Bounced) / float64(r.Sent)
		}
		result = append(result, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("per user stats rows: %w", err)
	}
	return result, nil
}
