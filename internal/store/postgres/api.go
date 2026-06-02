package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// FindingRow is a single DNS finding from a snapshot.
type FindingRow struct {
	Category string
	Severity string
	Code     string
	Message  string
	Detail   json.RawMessage
}

// DomainHealth is a combined view of a domain and its latest snapshot summary.
type DomainHealth struct {
	DomainID      string
	Name          string
	Status        string
	LastCheckedAt *time.Time
	SnapshotID    string // "" if the domain has no snapshot yet
	CapturedAt    *time.Time
	Checksum      string
	Healthy       bool
	FindingCount  int
	Findings      []FindingRow // populated by GetDomainHealth; nil in ListDomainHealth
}

const domainHealthBaseQuery = `
SELECT
    d.id,
    d.name,
    d.status::text,
    d.last_checked_at,
    s.id,
    s.captured_at,
    s.checksum,
    s.is_healthy,
    COALESCE((SELECT count(*) FROM dns_findings f WHERE f.snapshot_id = s.id), 0)
FROM domains d
LEFT JOIN LATERAL (
    SELECT id, captured_at, checksum, is_healthy
    FROM dns_snapshots
    WHERE domain_id = d.id
    ORDER BY captured_at DESC
    LIMIT 1
) s ON true
`

func scanDomainHealth(row pgx.Row) (DomainHealth, error) {
	var dh DomainHealth
	var snapID *string
	var capturedAt *time.Time
	var checksum *string
	var isHealthy *bool

	err := row.Scan(
		&dh.DomainID,
		&dh.Name,
		&dh.Status,
		&dh.LastCheckedAt,
		&snapID,
		&capturedAt,
		&checksum,
		&isHealthy,
		&dh.FindingCount,
	)
	if err != nil {
		return DomainHealth{}, err
	}

	if snapID != nil {
		dh.SnapshotID = *snapID
	}
	dh.CapturedAt = capturedAt
	if checksum != nil {
		dh.Checksum = *checksum
	}
	if isHealthy != nil {
		dh.Healthy = *isHealthy
	}
	return dh, nil
}

// ListDomainHealth returns every domain for a tenant with its latest-snapshot summary.
// Findings is left nil; FindingCount is set.
func (s *Store) ListDomainHealth(ctx context.Context, tenantID string) ([]DomainHealth, error) {
	const q = domainHealthBaseQuery + `WHERE d.tenant_id = $1 ORDER BY d.name`

	rows, err := s.Pool.Query(ctx, q, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list domain health: %w", err)
	}
	defer rows.Close()

	var out []DomainHealth
	for rows.Next() {
		dh, err := scanDomainHealth(rows)
		if err != nil {
			return nil, fmt.Errorf("scan domain health: %w", err)
		}
		out = append(out, dh)
	}
	return out, rows.Err()
}

// GetDomainHealth returns one domain (must belong to tenant) with its latest snapshot and
// full findings. found=false (nil error) if the domain doesn't exist for this tenant.
func (s *Store) GetDomainHealth(ctx context.Context, tenantID, domainID string) (dh DomainHealth, found bool, err error) {
	const q = domainHealthBaseQuery + `WHERE d.id = $2 AND d.tenant_id = $1`

	dh, err = scanDomainHealth(s.Pool.QueryRow(ctx, q, tenantID, domainID))
	if errors.Is(err, pgx.ErrNoRows) {
		return DomainHealth{}, false, nil
	}
	if err != nil {
		return DomainHealth{}, false, fmt.Errorf("get domain health: %w", err)
	}

	if dh.SnapshotID != "" {
		const fq = `SELECT category::text, severity::text, code, message, detail
		            FROM dns_findings
		            WHERE snapshot_id = $1
		            ORDER BY severity DESC, category`

		frows, ferr := s.Pool.Query(ctx, fq, dh.SnapshotID)
		if ferr != nil {
			return DomainHealth{}, false, fmt.Errorf("get domain findings: %w", ferr)
		}
		defer frows.Close()

		for frows.Next() {
			var fr FindingRow
			if serr := frows.Scan(&fr.Category, &fr.Severity, &fr.Code, &fr.Message, &fr.Detail); serr != nil {
				return DomainHealth{}, false, fmt.Errorf("scan finding: %w", serr)
			}
			dh.Findings = append(dh.Findings, fr)
		}
		if ferr = frows.Err(); ferr != nil {
			return DomainHealth{}, false, fmt.Errorf("iterate findings: %w", ferr)
		}
	}

	return dh, true, nil
}

// SnapshotSummary is a lightweight snapshot record for the drift timeline.
type SnapshotSummary struct {
	ID           string
	CapturedAt   time.Time
	Checksum     string
	Healthy      bool
	FindingCount int
}

// ListSnapshots returns a domain's snapshots newest-first (the drift timeline).
// found=false if the domain doesn't belong to the tenant.
func (s *Store) ListSnapshots(ctx context.Context, tenantID, domainID string, limit int) (snaps []SnapshotSummary, found bool, err error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}

	const verifyQ = `SELECT 1 FROM domains WHERE id = $1 AND tenant_id = $2`
	var dummy int
	if err = s.Pool.QueryRow(ctx, verifyQ, domainID, tenantID).Scan(&dummy); errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("verify domain ownership: %w", err)
	}

	const q = `
SELECT s.id, s.captured_at, s.checksum, s.is_healthy,
       (SELECT count(*) FROM dns_findings f WHERE f.snapshot_id = s.id)
FROM dns_snapshots s
WHERE s.domain_id = $1
ORDER BY captured_at DESC
LIMIT $2`

	rows, err := s.Pool.Query(ctx, q, domainID, limit)
	if err != nil {
		return nil, false, fmt.Errorf("list snapshots: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var ss SnapshotSummary
		if serr := rows.Scan(&ss.ID, &ss.CapturedAt, &ss.Checksum, &ss.Healthy, &ss.FindingCount); serr != nil {
			return nil, false, fmt.Errorf("scan snapshot: %w", serr)
		}
		snaps = append(snaps, ss)
	}
	return snaps, true, rows.Err()
}

// APICredential is a minimal projection of the api_credentials table.
type APICredential struct {
	ID          string
	TenantID    string
	Name        string
	TokenPrefix string
	TokenHash   string
	Scopes      []string
}

// CreateAPICredential inserts a new API credential and returns its generated id.
func (s *Store) CreateAPICredential(ctx context.Context, tenantID, name, tokenPrefix, tokenHash string, scopes []string) (string, error) {
	const q = `INSERT INTO api_credentials (tenant_id, name, token_prefix, token_hash, scopes)
	           VALUES ($1, $2, $3, $4, $5)
	           RETURNING id`
	var id string
	if err := s.Pool.QueryRow(ctx, q, tenantID, name, tokenPrefix, tokenHash, scopes).Scan(&id); err != nil {
		return "", fmt.Errorf("create api credential: %w", err)
	}
	return id, nil
}

// GetAPICredentialByPrefix looks up a non-revoked, unexpired credential by prefix for auth.
// found=false (nil error) if none matches.
func (s *Store) GetAPICredentialByPrefix(ctx context.Context, prefix string) (cred APICredential, found bool, err error) {
	const q = `SELECT id, tenant_id, name, token_prefix, token_hash, scopes
	           FROM api_credentials
	           WHERE token_prefix = $1
	             AND revoked_at IS NULL
	             AND (expires_at IS NULL OR expires_at > now())
	           LIMIT 1`

	err = s.Pool.QueryRow(ctx, q, prefix).Scan(
		&cred.ID, &cred.TenantID, &cred.Name, &cred.TokenPrefix, &cred.TokenHash, &cred.Scopes,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return APICredential{}, false, nil
	}
	if err != nil {
		return APICredential{}, false, fmt.Errorf("get api credential by prefix: %w", err)
	}
	return cred, true, nil
}

// TouchAPICredential sets last_used_at = now() for the given credential id.
func (s *Store) TouchAPICredential(ctx context.Context, id string) error {
	const q = `UPDATE api_credentials SET last_used_at = now() WHERE id = $1`
	if _, err := s.Pool.Exec(ctx, q, id); err != nil {
		return fmt.Errorf("touch api credential: %w", err)
	}
	return nil
}
