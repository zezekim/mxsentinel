package clickhouse

import (
	"context"
	"fmt"
	"time"
)

// DMARCPolicyDomain is per-domain message totals used to recommend a DMARC enforcement
// policy. PassMessages counts messages where DKIM or SPF was aligned (the DMARC pass
// condition), weighted by count.
type DMARCPolicyDomain struct {
	Domain        string
	TotalMessages uint64
	PassMessages  uint64
}

// DMARCPolicyObserved returns per-domain observed DMARC message totals over dmarc_records,
// ordered by total messages descending. FINAL collapses any rows the ReplacingMergeTree
// hasn't merged yet. The date-range filters are optional (zero time = unbounded).
func (s *Store) DMARCPolicyObserved(ctx context.Context, tenantID string, since, until time.Time) ([]DMARCPolicyDomain, error) {
	q := `SELECT domain,
	             sum(count) AS total,
	             sum(count * toUInt8(dkim_aligned = 1 OR spf_aligned = 1)) AS pass
	      FROM dmarc_records FINAL
	      WHERE tenant_id = ?`

	args := []any{tenantID}
	if !since.IsZero() {
		q += " AND date_begin >= ?"
		args = append(args, since)
	}
	if !until.IsZero() {
		q += " AND date_begin <= ?"
		args = append(args, until)
	}
	q += " GROUP BY domain ORDER BY total DESC"

	rows, err := s.conn.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("dmarc policy observed: %w", err)
	}
	defer rows.Close()

	var out []DMARCPolicyDomain
	for rows.Next() {
		var d DMARCPolicyDomain
		if err := rows.Scan(&d.Domain, &d.TotalMessages, &d.PassMessages); err != nil {
			return nil, fmt.Errorf("scan dmarc policy domain: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("dmarc policy observed: %w", err)
	}
	return out, nil
}
