package clickhouse

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// DomainMetrics holds per-domain email outcome counts for a time period.
type DomainMetrics struct {
	FromDomain string
	Delivered  uint64
	Deferred   uint64
	Bounced    uint64
	Rejected   uint64
	Total      uint64
}

// MetricsByDomain returns outcome counts grouped by from_domain for the given
// tenant and time window. If domains is non-empty, results are filtered to
// only those domains (max 500). Used to generate per-cPanel-account metrics.
func (s *Store) MetricsByDomain(ctx context.Context, tenantID string, domains []string, since, until time.Time) ([]DomainMetrics, error) {
	var b strings.Builder
	args := []any{tenantID}

	b.WriteString(`
SELECT
    from_domain,
    countIf(event_type = 'delivered') AS delivered,
    countIf(event_type = 'deferred')  AS deferred,
    countIf(event_type = 'bounced')   AS bounced,
    countIf(event_type = 'rejected')  AS rejected,
    count()                           AS total
FROM mxsentinel.smtp_events
WHERE tenant_id = ?`)

	args = appendTimeFilters(&b, args, since, until)

	if len(domains) > 0 && len(domains) <= 500 {
		placeholders := make([]string, len(domains))
		for i, d := range domains {
			args = append(args, d)
			placeholders[i] = "?"
		}
		fmt.Fprintf(&b, " AND from_domain IN (%s)", strings.Join(placeholders, ","))
	}

	b.WriteString(`
GROUP BY from_domain
ORDER BY total DESC`)

	rows, err := s.conn.Query(ctx, b.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("clickhouse MetricsByDomain: %w", err)
	}
	defer rows.Close()

	var out []DomainMetrics
	for rows.Next() {
		var m DomainMetrics
		if err := rows.Scan(&m.FromDomain, &m.Delivered, &m.Deferred, &m.Bounced, &m.Rejected, &m.Total); err != nil {
			return nil, fmt.Errorf("clickhouse MetricsByDomain scan: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("clickhouse MetricsByDomain rows: %w", err)
	}
	return out, nil
}

// AggregateByAccount sums DomainMetrics rows by cPanel account username.
// domainMap maps domain → account username. Returns map[username]DomainMetrics
// where values are summed across all domains for that account.
func AggregateByAccount(rows []DomainMetrics, domainMap map[string]string) map[string]DomainMetrics {
	result := map[string]DomainMetrics{}
	for _, r := range rows {
		username, ok := domainMap[r.FromDomain]
		if !ok {
			continue
		}
		agg := result[username]
		agg.FromDomain = username
		agg.Delivered += r.Delivered
		agg.Deferred += r.Deferred
		agg.Bounced += r.Bounced
		agg.Rejected += r.Rejected
		agg.Total += r.Total
		result[username] = agg
	}
	return result
}
