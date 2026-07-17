package clickhouse

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// DMARCTrendBucket is one daily bucket of DMARC message totals. Pass counts messages
// where DKIM or SPF was aligned (the DMARC pass condition), Fail the rest, weighted by
// count. Date is the bucket day (toDate(date_begin)).
type DMARCTrendBucket struct {
	Date  time.Time
	Total uint64
	Pass  uint64
	Fail  uint64
}

// DMARCTrend returns daily pass/fail message totals over dmarc_records, oldest first.
// FINAL collapses any rows the ReplacingMergeTree hasn't merged yet. The domain and
// date-range filters are optional (empty string / zero time = unbounded).
func (s *Store) DMARCTrend(ctx context.Context, tenantID, domain string, since, until time.Time) ([]DMARCTrendBucket, error) {
	var sb strings.Builder
	sb.WriteString(`SELECT toDate(date_begin) AS day,
	                       sum(count) AS total,
	                       sum(count * toUInt8(dkim_aligned = 1 OR spf_aligned = 1)) AS pass
	                FROM dmarc_records FINAL
	                WHERE tenant_id = ?`)

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

	sb.WriteString(" GROUP BY day ORDER BY day ASC")

	rows, err := s.conn.Query(ctx, sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("dmarc trend: %w", err)
	}
	defer rows.Close()

	var out []DMARCTrendBucket
	for rows.Next() {
		var b DMARCTrendBucket
		if err := rows.Scan(&b.Date, &b.Total, &b.Pass); err != nil {
			return nil, fmt.Errorf("scan dmarc trend bucket: %w", err)
		}
		b.Fail = b.Total - b.Pass
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("dmarc trend: %w", err)
	}
	return out, nil
}
