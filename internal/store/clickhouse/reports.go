package clickhouse

import (
	"context"
	"fmt"
	"time"
)

// ProviderFromDomainStat is per-receiver-provider outcome counts for mail SENT FROM one domain,
// used by the deliverability report to show where a customer's mail lands (Gmail vs Microsoft
// vs Yahoo …). Grouped by the recipient provider, filtered to one from_domain.
type ProviderFromDomainStat struct {
	Provider  string
	Delivered uint64
	Deferred  uint64
	Bounced   uint64
	Rejected  uint64
	Total     uint64
}

// ProviderBreakdownForFromDomain returns per-provider outcome counts for mail sent from the
// given from_domain in the window, most-volume first.
func (s *Store) ProviderBreakdownForFromDomain(ctx context.Context, tenantID, fromDomain string, since, until time.Time) ([]ProviderFromDomainStat, error) {
	const q = `
SELECT
    provider,
    countIf(event_type = 'delivered') AS delivered,
    countIf(event_type = 'deferred')  AS deferred,
    countIf(event_type = 'bounced')   AS bounced,
    countIf(event_type = 'rejected')  AS rejected,
    count()                           AS total
FROM mxsentinel.smtp_events
WHERE tenant_id = ? AND from_domain = ? AND event_time >= ? AND event_time <= ?
GROUP BY provider
ORDER BY total DESC`
	rows, err := s.conn.Query(ctx, q, tenantID, fromDomain, since, until)
	if err != nil {
		return nil, fmt.Errorf("clickhouse ProviderBreakdownForFromDomain: %w", err)
	}
	defer rows.Close()

	var out []ProviderFromDomainStat
	for rows.Next() {
		var p ProviderFromDomainStat
		if err := rows.Scan(&p.Provider, &p.Delivered, &p.Deferred, &p.Bounced, &p.Rejected, &p.Total); err != nil {
			return nil, fmt.Errorf("clickhouse ProviderBreakdownForFromDomain scan: %w", err)
		}
		if p.Provider == "" {
			p.Provider = "other"
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
