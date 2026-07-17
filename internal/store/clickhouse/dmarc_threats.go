package clickhouse

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"
)

// DMARCThreatRow is one spoofing source: a (source_ip, domain) pair whose messages
// failed both DKIM and SPF alignment. FailCount sums those failing messages,
// LastSeen is the most recent report window end, and TopDisposition is the
// disposition applied to the largest share of the group's messages.
type DMARCThreatRow struct {
	SourceIP       net.IP
	Domain         string
	FailCount      uint64
	LastSeen       time.Time
	TopDisposition string
}

// DMARCThreats returns the tenant's worst spoofing sources: rows where neither DKIM
// nor SPF aligned (dkim_aligned = 0 AND spf_aligned = 0), grouped by source IP and
// domain, ranked by failing-message volume. domain and since/until (when non-empty /
// non-zero) further constrain the scan. FINAL collapses any unmerged ReplacingMergeTree
// rows before aggregating.
func (s *Store) DMARCThreats(ctx context.Context, tenantID, domain string, since, until time.Time, limit int) ([]DMARCThreatRow, error) {
	var sb strings.Builder
	sb.WriteString(`SELECT source_ip,
	                       domain,
	                       sum(count) AS fail_count,
	                       max(date_end) AS last_seen,
	                       argMax(disposition, count) AS top_disposition
	                FROM dmarc_records FINAL
	                WHERE tenant_id = ? AND dkim_aligned = 0 AND spf_aligned = 0`)
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
	sb.WriteString(` GROUP BY source_ip, domain
	                 ORDER BY fail_count DESC
	                 LIMIT ?`)
	args = append(args, limit)

	rows, err := s.conn.Query(ctx, sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("dmarc threats: %w", err)
	}
	defer rows.Close()

	var out []DMARCThreatRow
	for rows.Next() {
		var t DMARCThreatRow
		if err := rows.Scan(&t.SourceIP, &t.Domain, &t.FailCount, &t.LastSeen, &t.TopDisposition); err != nil {
			return nil, fmt.Errorf("scan dmarc threat: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("dmarc threats: %w", err)
	}
	return out, nil
}
