package postgres

import (
	"context"
	"fmt"
	"time"
)

// DMARCReportCSVRow is the minimal set of report fields needed to build the
// reports CSV export. Domain is resolved like the reports listing:
// COALESCE(NULLIF(d.name,”), r.domain_name, ”).
type DMARCReportCSVRow struct {
	OrgName     string
	ReportID    string
	Domain      string
	DateBegin   time.Time
	DateEnd     time.Time
	RecordCount int
}

// ReportsForCSV returns a tenant's archived DMARC reports for CSV export,
// newest-first. domain/org (when non-empty) filter by domain name and exact
// org_name; since/until (when non-zero) bound the date_begin range.
func (s *Store) ReportsForCSV(ctx context.Context, tenantID, domain, org string, since, until time.Time) ([]DMARCReportCSVRow, error) {
	args := []any{tenantID}
	q := `SELECT r.org_name, r.report_id, COALESCE(NULLIF(d.name,''), r.domain_name, ''),
	             r.date_begin, r.date_end, r.record_count
	      FROM dmarc_reports r
	      LEFT JOIN domains d ON d.id = r.domain_id
	      WHERE r.tenant_id = $1`
	if domain != "" {
		args = append(args, domain)
		q += fmt.Sprintf(" AND d.name = $%d", len(args))
	}
	if org != "" {
		args = append(args, org)
		q += fmt.Sprintf(" AND r.org_name = $%d", len(args))
	}
	if !since.IsZero() {
		args = append(args, since)
		q += fmt.Sprintf(" AND r.date_begin >= $%d", len(args))
	}
	if !until.IsZero() {
		args = append(args, until)
		q += fmt.Sprintf(" AND r.date_begin <= $%d", len(args))
	}
	q += " ORDER BY r.date_begin DESC NULLS LAST"

	rows, err := s.Pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("reports for csv: %w", err)
	}
	defer rows.Close()

	var out []DMARCReportCSVRow
	for rows.Next() {
		var it DMARCReportCSVRow
		var begin, end *time.Time
		if err := rows.Scan(&it.OrgName, &it.ReportID, &it.Domain, &begin, &end, &it.RecordCount); err != nil {
			return nil, fmt.Errorf("scan report for csv: %w", err)
		}
		if begin != nil {
			it.DateBegin = *begin
		}
		if end != nil {
			it.DateEnd = *end
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reports for csv: %w", err)
	}
	return out, nil
}
