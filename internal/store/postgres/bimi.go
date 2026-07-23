package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// BIMISnapshot is one persisted BIMI/VMC readiness assessment (bimi_snapshots row).
type BIMISnapshot struct {
	ID            string
	TenantID      string
	DomainID      string
	DomainName    string // populated by the list/summary queries (joined from domains)
	Record        string
	LogoURL       string
	VMCURL        string
	VMCExpiry     *time.Time
	DMARCEnforced bool
	Readiness     string
	Checklist     json.RawMessage
	CheckedAt     time.Time
}

// BIMIDomain identifies a monitored domain for a tenant (used by the recheck handler).
type BIMIDomain struct {
	ID   string
	Name string
}

// GetBIMIDomain returns a domain's id+name for the tenant, found=false if it doesn't belong to
// the tenant.
func (s *Store) GetBIMIDomain(ctx context.Context, tenantID, domainID string) (BIMIDomain, bool, error) {
	const q = `SELECT id, name FROM domains WHERE id = $1 AND tenant_id = $2`
	var d BIMIDomain
	err := s.Pool.QueryRow(ctx, q, domainID, tenantID).Scan(&d.ID, &d.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return BIMIDomain{}, false, nil
	}
	if err != nil {
		return BIMIDomain{}, false, fmt.Errorf("get bimi domain: %w", err)
	}
	return d, true, nil
}

// LatestDMARCRecord returns the DMARC TXT record captured in a domain's most recent DNS
// snapshot, or "" when there is no snapshot or no DMARC record. BIMI reuses this instead of
// re-resolving DMARC.
func (s *Store) LatestDMARCRecord(ctx context.Context, domainID string) (string, error) {
	const q = `SELECT COALESCE(state->>'dmarc', '') FROM dns_snapshots
	           WHERE domain_id = $1 ORDER BY captured_at DESC LIMIT 1`
	var rec string
	err := s.Pool.QueryRow(ctx, q, domainID).Scan(&rec)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("latest dmarc record: %w", err)
	}
	return rec, nil
}

// InsertBIMISnapshot writes a new BIMI snapshot and returns its id.
func (s *Store) InsertBIMISnapshot(ctx context.Context, snap BIMISnapshot) (string, error) {
	checklist := snap.Checklist
	if len(checklist) == 0 {
		checklist = json.RawMessage("[]")
	}
	const q = `INSERT INTO bimi_snapshots
	    (tenant_id, domain_id, record, logo_url, vmc_url, vmc_expiry, dmarc_enforced, readiness_state, checklist_json)
	    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	    RETURNING id`
	var id string
	if err := s.Pool.QueryRow(ctx, q,
		snap.TenantID, snap.DomainID, snap.Record, snap.LogoURL, snap.VMCURL, snap.VMCExpiry,
		snap.DMARCEnforced, snap.Readiness, checklist,
	).Scan(&id); err != nil {
		return "", fmt.Errorf("insert bimi snapshot: %w", err)
	}
	return id, nil
}

// LatestBIMISnapshot returns a domain's most recent snapshot. found=false when none exists.
func (s *Store) LatestBIMISnapshot(ctx context.Context, domainID string) (BIMISnapshot, bool, error) {
	const q = `SELECT id, tenant_id, domain_id, record, logo_url, vmc_url, vmc_expiry,
	                  dmarc_enforced, readiness_state, checklist_json, checked_at
	           FROM bimi_snapshots WHERE domain_id = $1
	           ORDER BY checked_at DESC LIMIT 1`
	var b BIMISnapshot
	err := s.Pool.QueryRow(ctx, q, domainID).Scan(
		&b.ID, &b.TenantID, &b.DomainID, &b.Record, &b.LogoURL, &b.VMCURL, &b.VMCExpiry,
		&b.DMARCEnforced, &b.Readiness, &b.Checklist, &b.CheckedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return BIMISnapshot{}, false, nil
	}
	if err != nil {
		return BIMISnapshot{}, false, fmt.Errorf("latest bimi snapshot: %w", err)
	}
	return b, true, nil
}

// GetBIMISnapshotForTenant returns the latest snapshot for a domain, scoped to the tenant.
// found=false when the domain isn't the tenant's or has no snapshot.
func (s *Store) GetBIMISnapshotForTenant(ctx context.Context, tenantID, domainID string) (BIMISnapshot, bool, error) {
	const q = `SELECT b.id, b.tenant_id, b.domain_id, d.name, b.record, b.logo_url, b.vmc_url,
	                  b.vmc_expiry, b.dmarc_enforced, b.readiness_state, b.checklist_json, b.checked_at
	           FROM bimi_snapshots b
	           JOIN domains d ON d.id = b.domain_id
	           WHERE b.domain_id = $1 AND b.tenant_id = $2
	           ORDER BY b.checked_at DESC LIMIT 1`
	var b BIMISnapshot
	err := s.Pool.QueryRow(ctx, q, domainID, tenantID).Scan(
		&b.ID, &b.TenantID, &b.DomainID, &b.DomainName, &b.Record, &b.LogoURL, &b.VMCURL,
		&b.VMCExpiry, &b.DMARCEnforced, &b.Readiness, &b.Checklist, &b.CheckedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return BIMISnapshot{}, false, nil
	}
	if err != nil {
		return BIMISnapshot{}, false, fmt.Errorf("get bimi snapshot for tenant: %w", err)
	}
	return b, true, nil
}

// ListLatestBIMISnapshots returns the latest snapshot per domain for a tenant (the readiness
// summary). Domains with no snapshot yet are omitted; the API surfaces them as "unknown".
func (s *Store) ListLatestBIMISnapshots(ctx context.Context, tenantID string) ([]BIMISnapshot, error) {
	const q = `SELECT DISTINCT ON (b.domain_id)
	                  b.id, b.tenant_id, b.domain_id, d.name, b.record, b.logo_url, b.vmc_url,
	                  b.vmc_expiry, b.dmarc_enforced, b.readiness_state, b.checklist_json, b.checked_at
	           FROM bimi_snapshots b
	           JOIN domains d ON d.id = b.domain_id
	           WHERE b.tenant_id = $1
	           ORDER BY b.domain_id, b.checked_at DESC`
	rows, err := s.Pool.Query(ctx, q, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list latest bimi snapshots: %w", err)
	}
	defer rows.Close()

	var out []BIMISnapshot
	for rows.Next() {
		var b BIMISnapshot
		if err := rows.Scan(
			&b.ID, &b.TenantID, &b.DomainID, &b.DomainName, &b.Record, &b.LogoURL, &b.VMCURL,
			&b.VMCExpiry, &b.DMARCEnforced, &b.Readiness, &b.Checklist, &b.CheckedAt,
		); err != nil {
			return nil, fmt.Errorf("scan bimi snapshot: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
