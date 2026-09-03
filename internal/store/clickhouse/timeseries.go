package clickhouse

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// SeriesPoint is one time bucket of tenant-wide outcome counts.
type SeriesPoint struct {
	Bucket    time.Time
	Delivered uint64
	Deferred  uint64
	Bounced   uint64
	Rejected  uint64
	Total     uint64
}

// bucketExprs maps the caller-facing granularity name to the ClickHouse rounding
// function. Whitelisted deliberately: the value is interpolated into the query text
// (a function name can't be a bound parameter), so it must never come straight from
// user input.
var bucketExprs = map[string]string{
	"5m":   "toStartOfFiveMinutes",
	"hour": "toStartOfHour",
	"day":  "toStartOfDay",
}

// DeliverabilitySeries returns tenant-wide outcome counts bucketed over time, read from
// the deliverability_5m rollup rather than the raw event table — the rollup is ~12x
// smaller and already carries the four outcome sums we need.
//
// granularity is one of "5m", "hour", "day" (anything else is an error). since/until are
// optional; the zero value means unbounded on that side. Buckets with no traffic are
// absent from the result: ClickHouse emits no row where there were no events, so callers
// that need a dense series must fill the gaps themselves.
func (s *Store) DeliverabilitySeries(ctx context.Context, tenantID string, since, until time.Time, granularity string) ([]SeriesPoint, error) {
	fn, ok := bucketExprs[granularity]
	if !ok {
		return nil, fmt.Errorf("deliverability series: unknown granularity %q", granularity)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, `SELECT %s(bucket) AS b, sumMerge(delivered), sumMerge(deferred), sumMerge(bounced), sumMerge(rejected), sumMerge(total) FROM deliverability_5m WHERE tenant_id = ?`, fn)

	args := []any{tenantID}
	if !since.IsZero() {
		sb.WriteString(" AND bucket >= ?")
		args = append(args, since)
	}
	if !until.IsZero() {
		sb.WriteString(" AND bucket <= ?")
		args = append(args, until)
	}
	sb.WriteString(" GROUP BY b ORDER BY b ASC")

	rows, err := s.conn.Query(ctx, sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("deliverability series: %w", err)
	}
	defer rows.Close()

	var out []SeriesPoint
	for rows.Next() {
		var p SeriesPoint
		if err := rows.Scan(
			&p.Bucket,
			&p.Delivered,
			&p.Deferred,
			&p.Bounced,
			&p.Rejected,
			&p.Total,
		); err != nil {
			return nil, fmt.Errorf("deliverability series: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("deliverability series: %w", err)
	}
	return out, nil
}
