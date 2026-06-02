package postgres

import (
	"context"
	"errors"
	"fmt"

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
	    (tenant_id, domain_id, org_name, report_id, date_begin, date_end, object_key, record_count)
	    VALUES ($1, NULLIF($2,'')::uuid, $3, $4, $5, $6, $7, $8)
	    ON CONFLICT (tenant_id, org_name, report_id) DO NOTHING
	    RETURNING id`
	var id string
	err := s.Pool.QueryRow(ctx, q,
		p.TenantID, p.DomainID, p.OrgName, p.ReportID, p.DateBegin, p.DateEnd, p.ObjectKey, p.RecordCount,
	).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil // conflict: already inserted
	}
	if err != nil {
		return "", fmt.Errorf("insert dmarc report: %w", err)
	}
	return id, nil
}
