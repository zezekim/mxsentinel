package clickhouse

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"
)

// DMARCRecordRow is one row for dmarc_records. dmarcparser builds these from
// parsed DMARC aggregate XML reports. Enum columns take their string label;
// the SourceIP column takes net.IP (IPv4-mapped IPv6 is handled automatically).
type DMARCRecordRow struct {
	ReportID     string
	OrgName      string
	TenantID     string // UUID string
	Domain       string
	DateBegin    time.Time
	DateEnd      time.Time
	SourceIP     net.IP
	Count        uint32
	Disposition  string // "none"|"quarantine"|"reject" (enum label)
	DKIMAligned  uint8  // 0/1
	SPFAligned   uint8  // 0/1
	HeaderFrom   string
	EnvelopeFrom string
	IngestedAt   time.Time
}

const dmarcInsertStmt = `INSERT INTO dmarc_records (report_id, org_name, tenant_id, domain, date_begin, date_end, source_ip, count, disposition, dkim_aligned, spf_aligned, header_from, envelope_from, ingested_at)`

// InsertDMARCRecords writes a batch of parsed DMARC rows.
func (s *Store) InsertDMARCRecords(ctx context.Context, rows []DMARCRecordRow) error {
	if len(rows) == 0 {
		return nil
	}
	batch, err := s.conn.PrepareBatch(ctx, dmarcInsertStmt)
	if err != nil {
		return fmt.Errorf("prepare dmarc_records batch: %w", err)
	}
	for i := range rows {
		r := &rows[i]
		if err := batch.Append(
			r.ReportID, r.OrgName, r.TenantID, r.Domain,
			r.DateBegin, r.DateEnd,
			ipOrZero(r.SourceIP),
			r.Count, r.Disposition,
			r.DKIMAligned, r.SPFAligned,
			r.HeaderFrom, r.EnvelopeFrom,
			r.IngestedAt,
		); err != nil {
			return fmt.Errorf("append row %d: %w", i, err)
		}
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("send dmarc_records batch: %w", err)
	}
	return nil
}

// DMARCRecordDetail is one per-source row of a single report, for the report
// drill-down in the dashboard.
type DMARCRecordDetail struct {
	SourceIP     net.IP
	Count        uint32
	Disposition  string
	DKIMAligned  uint8
	SPFAligned   uint8
	HeaderFrom   string
	EnvelopeFrom string
}

// ListDMARCRecords returns the per-source rows of one report, identified by its
// (org_name, report_id) pair, largest sources first. FINAL collapses any rows the
// ReplacingMergeTree hasn't merged yet; per-report row counts are small.
func (s *Store) ListDMARCRecords(ctx context.Context, tenantID, orgName, reportID string) ([]DMARCRecordDetail, error) {
	const q = `SELECT source_ip, count, disposition, dkim_aligned, spf_aligned, header_from, envelope_from
	           FROM dmarc_records FINAL
	           WHERE tenant_id = ? AND org_name = ? AND report_id = ?
	           ORDER BY count DESC, source_ip`
	rows, err := s.conn.Query(ctx, q, tenantID, orgName, reportID)
	if err != nil {
		return nil, fmt.Errorf("list dmarc records: %w", err)
	}
	defer rows.Close()

	var out []DMARCRecordDetail
	for rows.Next() {
		var r DMARCRecordDetail
		if err := rows.Scan(&r.SourceIP, &r.Count, &r.Disposition, &r.DKIMAligned, &r.SPFAligned, &r.HeaderFrom, &r.EnvelopeFrom); err != nil {
			return nil, fmt.Errorf("scan dmarc record: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list dmarc records: %w", err)
	}
	return out, nil
}

// DMARCAnalyticsTotalsRow holds tenant-wide DMARC message totals: distinct reported
// domains, total messages, and messages passing/failing DMARC (DKIM or SPF aligned
// counts as a pass), weighted by count.
type DMARCAnalyticsTotalsRow struct {
	Domains  uint64
	Messages uint64
	Pass     uint64
	Fail     uint64
}

// DMARCAnalyticsTotals returns tenant-wide DMARC totals over dmarc_records in one query.
// since/until (when non-zero) bound the date_begin range.
func (s *Store) DMARCAnalyticsTotals(ctx context.Context, tenantID string, since, until time.Time) (DMARCAnalyticsTotalsRow, error) {
	var sb strings.Builder
	sb.WriteString(`SELECT count(DISTINCT domain),
	                       sum(count),
	                       sum(count * toUInt8(dkim_aligned = 1 OR spf_aligned = 1)),
	                       sum(count * toUInt8(dkim_aligned = 0 AND spf_aligned = 0))
	                FROM dmarc_records FINAL
	                WHERE tenant_id = ?`)
	args := []any{tenantID}
	if !since.IsZero() {
		sb.WriteString(" AND date_begin >= ?")
		args = append(args, since)
	}
	if !until.IsZero() {
		sb.WriteString(" AND date_begin <= ?")
		args = append(args, until)
	}
	var t DMARCAnalyticsTotalsRow
	if err := s.conn.QueryRow(ctx, sb.String(), args...).Scan(&t.Domains, &t.Messages, &t.Pass, &t.Fail); err != nil {
		return DMARCAnalyticsTotalsRow{}, fmt.Errorf("dmarc analytics totals: %w", err)
	}
	return t, nil
}

// DMARCFailingDomain is one domain's failure totals: Fails counts messages where neither
// DKIM nor SPF was aligned, Total all of the domain's messages, weighted by count.
type DMARCFailingDomain struct {
	Domain string
	Fails  uint64
	Total  uint64
}

// DMARCTopFailingDomains returns the tenant's domains with the most DMARC-failing
// messages, worst first. Domains with no failures are omitted. since/until (when
// non-zero) bound the date_begin range.
func (s *Store) DMARCTopFailingDomains(ctx context.Context, tenantID string, since, until time.Time, limit int) ([]DMARCFailingDomain, error) {
	var sb strings.Builder
	sb.WriteString(`SELECT domain,
	                       sum(count * toUInt8(dkim_aligned = 0 AND spf_aligned = 0)) AS fails,
	                       sum(count) AS total
	                FROM dmarc_records FINAL
	                WHERE tenant_id = ?`)
	args := []any{tenantID}
	if !since.IsZero() {
		sb.WriteString(" AND date_begin >= ?")
		args = append(args, since)
	}
	if !until.IsZero() {
		sb.WriteString(" AND date_begin <= ?")
		args = append(args, until)
	}
	sb.WriteString(` GROUP BY domain
	                 HAVING fails > 0
	                 ORDER BY fails DESC
	                 LIMIT ?`)
	args = append(args, limit)
	rows, err := s.conn.Query(ctx, sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("dmarc top failing domains: %w", err)
	}
	defer rows.Close()

	var out []DMARCFailingDomain
	for rows.Next() {
		var d DMARCFailingDomain
		if err := rows.Scan(&d.Domain, &d.Fails, &d.Total); err != nil {
			return nil, fmt.Errorf("scan dmarc failing domain: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("dmarc top failing domains: %w", err)
	}
	return out, nil
}

// DMARCReportStat holds per-report message totals: Pass counts messages where DKIM
// or SPF was aligned (the DMARC pass condition), Fail the rest, weighted by count.
type DMARCReportStat struct {
	Pass uint64
	Fail uint64
}

// DMARCReportStatKey identifies a report in dmarc_records.
type DMARCReportStatKey struct {
	OrgName  string
	ReportID string
}

// DMARCReportStats returns pass/fail message counts for the given reports, keyed by
// (org_name, report_id). Reports with no rows in ClickHouse are absent from the map.
func (s *Store) DMARCReportStats(ctx context.Context, tenantID string, keys []DMARCReportStatKey) (map[DMARCReportStatKey]DMARCReportStat, error) {
	if len(keys) == 0 {
		return map[DMARCReportStatKey]DMARCReportStat{}, nil
	}
	var sb strings.Builder
	sb.WriteString(`SELECT org_name, report_id,
	                       sum(count * toUInt8(dkim_aligned = 1 OR spf_aligned = 1)),
	                       sum(count * toUInt8(dkim_aligned = 0 AND spf_aligned = 0))
	                FROM dmarc_records FINAL
	                WHERE tenant_id = ? AND (org_name, report_id) IN (`)
	args := []any{tenantID}
	for i, k := range keys {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString("(?,?)")
		args = append(args, k.OrgName, k.ReportID)
	}
	sb.WriteString(") GROUP BY org_name, report_id")

	rows, err := s.conn.Query(ctx, sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("dmarc report stats: %w", err)
	}
	defer rows.Close()

	out := make(map[DMARCReportStatKey]DMARCReportStat, len(keys))
	for rows.Next() {
		var k DMARCReportStatKey
		var st DMARCReportStat
		if err := rows.Scan(&k.OrgName, &k.ReportID, &st.Pass, &st.Fail); err != nil {
			return nil, fmt.Errorf("scan dmarc report stat: %w", err)
		}
		out[k] = st
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("dmarc report stats: %w", err)
	}
	return out, nil
}
