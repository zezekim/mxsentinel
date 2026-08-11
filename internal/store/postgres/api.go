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

// APICredentialInfo is a credential as listed for operators: everything except the secret.
// Timestamps are RFC3339, or "" when the underlying column is NULL.
type APICredentialInfo struct {
	ID          string
	Name        string
	TokenPrefix string
	Scopes      []string
	CreatedAt   string
	LastUsedAt  string // "" if never used
	ExpiresAt   string // "" if it never expires
	RevokedAt   string // "" if still active
}

// CreateAPICredential inserts a new API credential and returns its generated id.
// expiresAt is optional; nil means the credential never expires.
func (s *Store) CreateAPICredential(ctx context.Context, tenantID, name, tokenPrefix, tokenHash string, scopes []string, expiresAt *time.Time) (string, error) {
	const q = `INSERT INTO api_credentials (tenant_id, name, token_prefix, token_hash, scopes, expires_at)
	           VALUES ($1, $2, $3, $4, $5, $6)
	           RETURNING id`
	var id string
	if err := s.Pool.QueryRow(ctx, q, tenantID, name, tokenPrefix, tokenHash, scopes, expiresAt).Scan(&id); err != nil {
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

// ListAPICredentials returns every credential in the tenant, revoked ones included, newest
// first. Revoked credentials are kept in the listing deliberately: operators auditing a
// decommissioned server need to confirm its key is actually dead.
func (s *Store) ListAPICredentials(ctx context.Context, tenantID string) ([]APICredentialInfo, error) {
	const q = `SELECT id, name, token_prefix, scopes, created_at, last_used_at, expires_at, revoked_at
	           FROM api_credentials
	           WHERE tenant_id = $1
	           ORDER BY created_at DESC`
	rows, err := s.Pool.Query(ctx, q, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list api credentials: %w", err)
	}
	defer rows.Close()

	var out []APICredentialInfo
	for rows.Next() {
		var (
			c                          APICredentialInfo
			created                    time.Time
			lastUsed, expires, revoked *time.Time
		)
		if err := rows.Scan(&c.ID, &c.Name, &c.TokenPrefix, &c.Scopes, &created, &lastUsed, &expires, &revoked); err != nil {
			return nil, fmt.Errorf("scan api credential: %w", err)
		}
		c.CreatedAt = created.UTC().Format(time.RFC3339)
		c.LastUsedAt = formatNullTime(lastUsed)
		c.ExpiresAt = formatNullTime(expires)
		c.RevokedAt = formatNullTime(revoked)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list api credentials: %w", err)
	}
	return out, nil
}

// RevokeAPICredentialByName marks a credential revoked by its per-tenant name. found=false
// (nil error) if the tenant has no credential by that name. Revoking an already-revoked
// credential is a no-op that still reports found=true, so callers can be run repeatedly.
func (s *Store) RevokeAPICredentialByName(ctx context.Context, tenantID, name string) (bool, error) {
	const q = `UPDATE api_credentials
	           SET revoked_at = COALESCE(revoked_at, now())
	           WHERE tenant_id = $1 AND name = $2`
	tag, err := s.Pool.Exec(ctx, q, tenantID, name)
	if err != nil {
		return false, fmt.Errorf("revoke api credential by name: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// RevokeAPICredentialByID marks a credential revoked. The tenant is part of the predicate so
// one tenant can never revoke another's credential by guessing an id.
func (s *Store) RevokeAPICredentialByID(ctx context.Context, tenantID, id string) (bool, error) {
	const q = `UPDATE api_credentials
	           SET revoked_at = COALESCE(revoked_at, now())
	           WHERE tenant_id = $1 AND id = $2`
	tag, err := s.Pool.Exec(ctx, q, tenantID, id)
	if err != nil {
		return false, fmt.Errorf("revoke api credential by id: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ReissueAPICredential atomically replaces the tenant's credential of this name with a fresh
// secret, reporting whether one was replaced. It exists because UNIQUE (tenant_id, name)
// means revoking a credential does NOT free its name — so re-enrolling a rebuilt server
// would otherwise collide forever. The old row is revoked and renamed rather than deleted,
// which keeps an audit trail of every secret that was ever valid for that server.
//
// replaceableScopes bounds which existing credentials may be displaced: only a row whose
// scopes are contained in that set is archived. Without it, an enrollment token could pick
// the name of an operator's own broader credential and revoke it — no privilege gain, but a
// free way to knock out someone else's access. A row that exists and is NOT replaceable
// keeps the name, so the insert below fails on the unique constraint and the caller sees a
// conflict rather than a silent overwrite.
//
// Both steps run in one transaction: a partial failure that revoked the old credential
// without issuing a new one would lock the server out of the API.
func (s *Store) ReissueAPICredential(ctx context.Context, tenantID, name, tokenPrefix, tokenHash string, scopes []string, expiresAt *time.Time, replaceableScopes []string) (id string, replaced bool, err error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return "", false, fmt.Errorf("reissue api credential: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Microsecond precision in the archived name keeps it unique against every prior
	// archive of the same base name. `<@` is Postgres array containment.
	const archive = `UPDATE api_credentials
	                 SET revoked_at = COALESCE(revoked_at, now()),
	                     name = name || '-revoked-' || to_char(now() AT TIME ZONE 'UTC', 'YYYYMMDDHH24MISSUS')
	                 WHERE tenant_id = $1 AND name = $2 AND scopes <@ $3::text[]`
	tag, err := tx.Exec(ctx, archive, tenantID, name, replaceableScopes)
	if err != nil {
		return "", false, fmt.Errorf("reissue api credential: archive: %w", err)
	}
	replaced = tag.RowsAffected() > 0

	const insert = `INSERT INTO api_credentials (tenant_id, name, token_prefix, token_hash, scopes, expires_at)
	                VALUES ($1, $2, $3, $4, $5, $6)
	                RETURNING id`
	if err := tx.QueryRow(ctx, insert, tenantID, name, tokenPrefix, tokenHash, scopes, expiresAt).Scan(&id); err != nil {
		return "", false, fmt.Errorf("reissue api credential: insert: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", false, fmt.Errorf("reissue api credential: commit: %w", err)
	}
	return id, replaced, nil
}

func formatNullTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// TouchAPICredential sets last_used_at = now() for the given credential id.
func (s *Store) TouchAPICredential(ctx context.Context, id string) error {
	const q = `UPDATE api_credentials SET last_used_at = now() WHERE id = $1`
	if _, err := s.Pool.Exec(ctx, q, id); err != nil {
		return fmt.Errorf("touch api credential: %w", err)
	}
	return nil
}
