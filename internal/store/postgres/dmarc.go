package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// GetDomainID returns the id of a tenant's domain by name. The bool is false (nil error)
// when the tenant has no such domain.
func (s *Store) GetDomainID(ctx context.Context, tenantID, name string) (string, bool, error) {
	const q = `SELECT id FROM domains WHERE tenant_id = $1 AND name = $2 LIMIT 1`
	var id string
	err := s.Pool.QueryRow(ctx, q, tenantID, name).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("get domain id: %w", err)
	}
	return id, true, nil
}

// DMARCReportExists reports whether a (tenant, org, report_id) DMARC report is already
// recorded — the dedupe check before archiving/parsing.
func (s *Store) DMARCReportExists(ctx context.Context, tenantID, orgName, reportID string) (bool, error) {
	const q = `SELECT 1 FROM dmarc_reports
	           WHERE tenant_id = $1 AND org_name = $2 AND report_id = $3 LIMIT 1`
	var one int
	err := s.Pool.QueryRow(ctx, q, tenantID, orgName, reportID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("dmarc report exists: %w", err)
	}
	return true, nil
}

// DMARCReportPointer is the index row pointing at a raw report archived in object storage.
type DMARCReportPointer struct {
	TenantID    string
	DomainID    string // may be "" (NULL)
	DomainName  string // plain-text fallback when DomainID is empty
	OrgName     string
	ReportID    string
	DateBegin   any // time.Time
	DateEnd     any // time.Time
	ObjectKey   string
	RecordCount int
}

// InsertDMARCReport inserts the pointer row. It is a no-op on conflict with an existing
// (tenant, org, report_id); the returned id is empty in that case.
func (s *Store) InsertDMARCReport(ctx context.Context, p DMARCReportPointer) (string, error) {
	const q = `INSERT INTO dmarc_reports
	    (tenant_id, domain_id, domain_name, org_name, report_id, date_begin, date_end, object_key, record_count)
	    VALUES ($1, NULLIF($2,'')::uuid, $3, $4, $5, $6, $7, $8, $9)
	    ON CONFLICT (tenant_id, org_name, report_id) DO NOTHING
	    RETURNING id`
	var id string
	err := s.Pool.QueryRow(ctx, q,
		p.TenantID, p.DomainID, p.DomainName, p.OrgName, p.ReportID, p.DateBegin, p.DateEnd, p.ObjectKey, p.RecordCount,
	).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil // conflict: already inserted
	}
	if err != nil {
		return "", fmt.Errorf("insert dmarc report: %w", err)
	}
	return id, nil
}

// CountDMARCReports returns how many DMARC reports are archived for the tenant.
// since/until (when non-zero) bound the date_begin range.
func (s *Store) CountDMARCReports(ctx context.Context, tenantID string, since, until time.Time) (int, error) {
	args := []any{tenantID}
	q := `SELECT count(*) FROM dmarc_reports WHERE tenant_id = $1`
	if !since.IsZero() {
		args = append(args, since)
		q += fmt.Sprintf(" AND date_begin >= $%d", len(args))
	}
	if !until.IsZero() {
		args = append(args, until)
		q += fmt.Sprintf(" AND date_begin <= $%d", len(args))
	}
	var n int
	if err := s.Pool.QueryRow(ctx, q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count dmarc reports: %w", err)
	}
	return n, nil
}

// DMARCReportListItem is a row for the reports listing (joins the domain name).
type DMARCReportListItem struct {
	ID          string
	OrgName     string
	ReportID    string
	Domain      string
	DateBegin   time.Time
	DateEnd     time.Time
	RecordCount int
}

// GetDMARCReport fetches one of a tenant's reports by row id. The bool is false
// (nil error) when no such report exists for the tenant.
func (s *Store) GetDMARCReport(ctx context.Context, tenantID, id string) (DMARCReportListItem, bool, error) {
	const q = `SELECT r.id, r.org_name, r.report_id, COALESCE(NULLIF(d.name,''), r.domain_name, ''),
	                  r.date_begin, r.date_end, r.record_count
	           FROM dmarc_reports r
	           LEFT JOIN domains d ON d.id = r.domain_id
	           WHERE r.tenant_id = $1 AND r.id = $2`
	var it DMARCReportListItem
	var begin, end *time.Time
	err := s.Pool.QueryRow(ctx, q, tenantID, id).Scan(&it.ID, &it.OrgName, &it.ReportID, &it.Domain, &begin, &end, &it.RecordCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return DMARCReportListItem{}, false, nil
	}
	if err != nil {
		return DMARCReportListItem{}, false, fmt.Errorf("get dmarc report: %w", err)
	}
	if begin != nil {
		it.DateBegin = *begin
	}
	if end != nil {
		it.DateEnd = *end
	}
	return it, true, nil
}

// DMARCReportFilter narrows and orders the reports listing. Zero-value fields are
// ignored. Order (asc|desc) applies to the date_begin sort done here; pass/fail/total
// ordering is handled by the caller after ClickHouse enrichment.
type DMARCReportFilter struct {
	Domain string
	Org    string // exact org_name match
	Since  time.Time
	Until  time.Time
	Order  string // "asc" or "desc" (default desc)
	Limit  int
}

// ListDMARCReports lists a tenant's archived DMARC reports newest-first, optionally
// filtered by domain name, org name, and a date_begin range.
func (s *Store) ListDMARCReports(ctx context.Context, tenantID string, f DMARCReportFilter) ([]DMARCReportListItem, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	args := []any{tenantID}
	q := `SELECT r.id, r.org_name, r.report_id, COALESCE(NULLIF(d.name,''), r.domain_name, ''),
	             r.date_begin, r.date_end, r.record_count
	      FROM dmarc_reports r
	      LEFT JOIN domains d ON d.id = r.domain_id
	      WHERE r.tenant_id = $1`
	if f.Domain != "" {
		args = append(args, f.Domain)
		q += fmt.Sprintf(" AND d.name = $%d", len(args))
	}
	if f.Org != "" {
		args = append(args, f.Org)
		q += fmt.Sprintf(" AND r.org_name = $%d", len(args))
	}
	if !f.Since.IsZero() {
		args = append(args, f.Since)
		q += fmt.Sprintf(" AND r.date_begin >= $%d", len(args))
	}
	if !f.Until.IsZero() {
		args = append(args, f.Until)
		q += fmt.Sprintf(" AND r.date_begin <= $%d", len(args))
	}
	dir := "DESC"
	if f.Order == "asc" {
		dir = "ASC"
	}
	args = append(args, limit)
	q += fmt.Sprintf(" ORDER BY r.date_begin %s NULLS LAST LIMIT $%d", dir, len(args))

	rows, err := s.Pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list dmarc reports: %w", err)
	}
	defer rows.Close()

	var out []DMARCReportListItem
	for rows.Next() {
		var it DMARCReportListItem
		var begin, end *time.Time
		if err := rows.Scan(&it.ID, &it.OrgName, &it.ReportID, &it.Domain, &begin, &end, &it.RecordCount); err != nil {
			return nil, fmt.Errorf("scan dmarc report: %w", err)
		}
		if begin != nil {
			it.DateBegin = *begin
		}
		if end != nil {
			it.DateEnd = *end
		}
		out = append(out, it)
	}
	return out, rows.Err()
}
