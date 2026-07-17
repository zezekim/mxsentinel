package clickhouse

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"
)

// DMARCSourceRow is one sending source's DMARC totals: Pass counts messages where DKIM
// or SPF was aligned (the DMARC pass condition), Fail the rest, weighted by count.
type DMARCSourceRow struct {
	SourceIP net.IP
	Total    uint64
	Pass     uint64
	Fail     uint64
}

// DMARCTopSources returns the tenant's sending source IPs with the most DMARC-reported
// messages, busiest first. domain and the since/until date_begin range (when non-empty /
// non-zero) further bound the rows. FINAL collapses any unmerged ReplacingMergeTree rows.
func (s *Store) DMARCTopSources(ctx context.Context, tenantID, domain string, since, until time.Time, limit int) ([]DMARCSourceRow, error) {
	var sb strings.Builder
	sb.WriteString(`SELECT source_ip,
	                       sum(count) AS total,
	                       sum(count * toUInt8(dkim_aligned = 1 OR spf_aligned = 1)) AS pass,
	                       sum(count * toUInt8(dkim_aligned = 0 AND spf_aligned = 0)) AS fail
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
	sb.WriteString(` GROUP BY source_ip
	                 ORDER BY total DESC
	                 LIMIT ?`)
	args = append(args, limit)

	rows, err := s.conn.Query(ctx, sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("dmarc top sources: %w", err)
	}
	defer rows.Close()

	var out []DMARCSourceRow
	for rows.Next() {
		var src DMARCSourceRow
		if err := rows.Scan(&src.SourceIP, &src.Total, &src.Pass, &src.Fail); err != nil {
			return nil, fmt.Errorf("scan dmarc source: %w", err)
		}
		out = append(out, src)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("dmarc top sources: %w", err)
	}
	return out, nil
}
