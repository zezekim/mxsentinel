package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ─── MTA-STS snapshots ──────────────────────────────────────────────────────────

// MTASTSSnapshot is a per-domain MTA-STS policy snapshot to persist. Findings and mx hosts
// are stored as JSONB. StateJSON is the serialized mtasts.State (change-detection source).
type MTASTSSnapshot struct {
	TenantID   string
	DomainID   string // may be "" (NULL)
	DomainName string
	Mode       string
	MaxAge     int
	MXHosts    []string
	Checksum   string
	CertExpiry *time.Time // nil when no valid cert observed
	IsHealthy  bool
	StateJSON  []byte // serialized mtasts.State
	Findings   any    // marshaled to JSONB (e.g. []contracts.DNSFinding)
}

// LatestMTASTSChecksum returns the checksum of a domain's most recent MTA-STS snapshot, or
// "" if there is none.
func (s *Store) LatestMTASTSChecksum(ctx context.Context, domainID string) (string, error) {
	const q = `SELECT checksum FROM mtasts_snapshots WHERE domain_id = $1
	           ORDER BY captured_at DESC LIMIT 1`
	var sum string
	err := s.Pool.QueryRow(ctx, q, domainID).Scan(&sum)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("latest mtasts checksum: %w", err)
	}
	return sum, nil
}

// InsertMTASTSSnapshot writes a new snapshot row and returns its id.
func (s *Store) InsertMTASTSSnapshot(ctx context.Context, snap MTASTSSnapshot) (string, error) {
	mxJSON, err := json.Marshal(snap.MXHosts)
	if err != nil {
		return "", fmt.Errorf("marshal mx hosts: %w", err)
	}
	findingsJSON, err := json.Marshal(orEmptyList(snap.Findings))
	if err != nil {
		return "", fmt.Errorf("marshal findings: %w", err)
	}
	stateJSON := snap.StateJSON
	if len(stateJSON) == 0 {
		stateJSON = []byte("{}")
	}
	const q = `INSERT INTO mtasts_snapshots
	    (tenant_id, domain_id, domain_name, mode, max_age, mx_hosts, checksum, cert_expiry, is_healthy, state, findings)
	    VALUES ($1, NULLIF($2,'')::uuid, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	    RETURNING id`
	var id string
	err = s.Pool.QueryRow(ctx, q,
		snap.TenantID, snap.DomainID, snap.DomainName, snap.Mode, snap.MaxAge,
		mxJSON, snap.Checksum, snap.CertExpiry, snap.IsHealthy, stateJSON, findingsJSON,
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("insert mtasts snapshot: %w", err)
	}
	return id, nil
}

// MTASTSSnapshotItem is a row for the MTA-STS listing.
type MTASTSSnapshotItem struct {
	ID         string
	DomainID   string
	Domain     string
	Mode       string
	MaxAge     int
	MXHosts    []string
	Checksum   string
	CertExpiry *time.Time
	IsHealthy  bool
	Findings   json.RawMessage
	CapturedAt time.Time
}

// ListMTASTSSnapshots returns the latest MTA-STS snapshot per domain for a tenant.
func (s *Store) ListMTASTSSnapshots(ctx context.Context, tenantID string) ([]MTASTSSnapshotItem, error) {
	const q = `SELECT DISTINCT ON (domain_id, domain_name)
	              id, COALESCE(domain_id::text,''), domain_name, mode, max_age, mx_hosts,
	              checksum, cert_expiry, is_healthy, findings, captured_at
	           FROM mtasts_snapshots
	           WHERE tenant_id = $1
	           ORDER BY domain_id, domain_name, captured_at DESC`
	rows, err := s.Pool.Query(ctx, q, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list mtasts snapshots: %w", err)
	}
	defer rows.Close()

	var out []MTASTSSnapshotItem
	for rows.Next() {
		it, err := scanMTASTSItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// GetMTASTSSnapshot returns a domain's latest MTA-STS snapshot. found=false when the domain
// does not belong to the tenant or has no snapshot yet.
func (s *Store) GetMTASTSSnapshot(ctx context.Context, tenantID, domainID string) (MTASTSSnapshotItem, bool, error) {
	const verifyQ = `SELECT 1 FROM domains WHERE id = $1 AND tenant_id = $2`
	var dummy int
	if err := s.Pool.QueryRow(ctx, verifyQ, domainID, tenantID).Scan(&dummy); errors.Is(err, pgx.ErrNoRows) {
		return MTASTSSnapshotItem{}, false, nil
	} else if err != nil {
		return MTASTSSnapshotItem{}, false, fmt.Errorf("verify domain ownership: %w", err)
	}

	const q = `SELECT id, COALESCE(domain_id::text,''), domain_name, mode, max_age, mx_hosts,
	              checksum, cert_expiry, is_healthy, findings, captured_at
	           FROM mtasts_snapshots
	           WHERE tenant_id = $1 AND domain_id = $2
	           ORDER BY captured_at DESC LIMIT 1`
	rows, err := s.Pool.Query(ctx, q, tenantID, domainID)
	if err != nil {
		return MTASTSSnapshotItem{}, false, fmt.Errorf("get mtasts snapshot: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return MTASTSSnapshotItem{}, false, rows.Err()
	}
	it, err := scanMTASTSItem(rows)
	if err != nil {
		return MTASTSSnapshotItem{}, false, err
	}
	return it, true, nil
}

func scanMTASTSItem(rows pgx.Rows) (MTASTSSnapshotItem, error) {
	var it MTASTSSnapshotItem
	var mxJSON []byte
	if err := rows.Scan(&it.ID, &it.DomainID, &it.Domain, &it.Mode, &it.MaxAge, &mxJSON,
		&it.Checksum, &it.CertExpiry, &it.IsHealthy, &it.Findings, &it.CapturedAt); err != nil {
		return MTASTSSnapshotItem{}, fmt.Errorf("scan mtasts snapshot: %w", err)
	}
	if len(mxJSON) > 0 {
		_ = json.Unmarshal(mxJSON, &it.MXHosts)
	}
	return it, nil
}

// ─── TLS-RPT report pointers ────────────────────────────────────────────────────

// TLSRPTReportPointer is the index row pointing at a raw TLS-RPT report archived in object
// storage.
type TLSRPTReportPointer struct {
	TenantID     string
	DomainID     string // may be "" (NULL)
	DomainName   string
	OrgName      string
	ReportID     string
	DateBegin    any // time.Time
	DateEnd      any // time.Time
	ObjectKey    string
	PolicyCount  int
	SuccessCount uint64
	FailureCount uint64
}

// TLSRPTReportExists reports whether a (tenant, report_id) TLS-RPT report is already
// recorded — the dedupe check before archiving/parsing.
func (s *Store) TLSRPTReportExists(ctx context.Context, tenantID, reportID string) (bool, error) {
	const q = `SELECT 1 FROM tlsrpt_reports WHERE tenant_id = $1 AND report_id = $2 LIMIT 1`
	var one int
	err := s.Pool.QueryRow(ctx, q, tenantID, reportID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("tlsrpt report exists: %w", err)
	}
	return true, nil
}

// InsertTLSRPTReport inserts the pointer row. It is a no-op on conflict with an existing
// (tenant, report_id); the returned id is empty in that case.
func (s *Store) InsertTLSRPTReport(ctx context.Context, p TLSRPTReportPointer) (string, error) {
	const q = `INSERT INTO tlsrpt_reports
	    (tenant_id, domain_id, domain_name, org_name, report_id, date_begin, date_end, object_key, policy_count, success_count, failure_count)
	    VALUES ($1, NULLIF($2,'')::uuid, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	    ON CONFLICT (tenant_id, report_id) DO NOTHING
	    RETURNING id`
	var id string
	err := s.Pool.QueryRow(ctx, q,
		p.TenantID, p.DomainID, p.DomainName, p.OrgName, p.ReportID,
		p.DateBegin, p.DateEnd, p.ObjectKey, p.PolicyCount, int64(p.SuccessCount), int64(p.FailureCount),
	).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil // conflict: already inserted
	}
	if err != nil {
		return "", fmt.Errorf("insert tlsrpt report: %w", err)
	}
	return id, nil
}

// TLSRPTReportItem is a row for the TLS-RPT reports listing.
type TLSRPTReportItem struct {
	ID           string
	OrgName      string
	ReportID     string
	Domain       string
	DateBegin    time.Time
	DateEnd      time.Time
	PolicyCount  int
	SuccessCount uint64
	FailureCount uint64
}

// ListTLSRPTReports lists a tenant's archived TLS-RPT reports newest-first, optionally
// filtered by domain name.
func (s *Store) ListTLSRPTReports(ctx context.Context, tenantID, domain string, limit int) ([]TLSRPTReportItem, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	args := []any{tenantID}
	q := `SELECT r.id, r.org_name, r.report_id,
	             COALESCE(NULLIF(d.name,''), r.domain_name, ''),
	             r.date_begin, r.date_end, r.policy_count, r.success_count, r.failure_count
	      FROM tlsrpt_reports r
	      LEFT JOIN domains d ON d.id = r.domain_id
	      WHERE r.tenant_id = $1`
	if domain != "" {
		args = append(args, domain)
		q += fmt.Sprintf(" AND (d.name = $%d OR r.domain_name = $%d)", len(args), len(args))
	}
	args = append(args, limit)
	q += fmt.Sprintf(" ORDER BY r.date_begin DESC NULLS LAST LIMIT $%d", len(args))

	rows, err := s.Pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list tlsrpt reports: %w", err)
	}
	defer rows.Close()

	var out []TLSRPTReportItem
	for rows.Next() {
		var it TLSRPTReportItem
		var begin, end *time.Time
		var success, failure int64
		if err := rows.Scan(&it.ID, &it.OrgName, &it.ReportID, &it.Domain,
			&begin, &end, &it.PolicyCount, &success, &failure); err != nil {
			return nil, fmt.Errorf("scan tlsrpt report: %w", err)
		}
		if begin != nil {
			it.DateBegin = *begin
		}
		if end != nil {
			it.DateEnd = *end
		}
		it.SuccessCount = uint64(success)
		it.FailureCount = uint64(failure)
		out = append(out, it)
	}
	return out, rows.Err()
}

func orEmptyList(v any) any {
	if v == nil {
		return []any{}
	}
	return v
}
