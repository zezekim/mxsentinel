package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ShareLink is a capability link to one message's delivery trace. Only token_hash and
// token_prefix are persisted for the secret; the plaintext token is shown once at creation.
type ShareLink struct {
	ID           string
	TenantID     string
	QueueID      string
	MessageID    string
	TokenPrefix  string
	TokenHash    string
	Label        string
	CreatedBy    string // "" when minted by an API token (no user session)
	ExpiresAt    *time.Time
	RevokedAt    *time.Time
	LastViewedAt *time.Time
	ViewCount    int
	CreatedAt    time.Time
}

// Active reports whether the link is currently usable (not revoked, not expired).
func (l ShareLink) Active() bool {
	if l.RevokedAt != nil {
		return false
	}
	if l.ExpiresAt != nil && l.ExpiresAt.Before(time.Now()) {
		return false
	}
	return true
}

// CreateShareLink inserts a new share link and returns its generated id. createdBy may be ""
// (stored as NULL) when the caller authenticated with an API token rather than a user login.
func (s *Store) CreateShareLink(ctx context.Context, tenantID, queueID, messageID, tokenPrefix, tokenHash, label, createdBy string, expiresAt *time.Time) (string, error) {
	const q = `INSERT INTO message_share_links
	           (tenant_id, queue_id, message_id, token_prefix, token_hash, label, created_by, expires_at)
	           VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	           RETURNING id`
	var createdByArg any
	if createdBy != "" {
		createdByArg = createdBy
	}
	var id string
	if err := s.Pool.QueryRow(ctx, q, tenantID, queueID, messageID, tokenPrefix, tokenHash, label, createdByArg, expiresAt).Scan(&id); err != nil {
		return "", fmt.Errorf("create share link: %w", err)
	}
	return id, nil
}

// GetShareLinkByPrefix looks up a share link by its non-secret lookup prefix, for resolving a
// public trace request. It returns links regardless of revoked/expired state so the caller
// (public handler) can distinguish "gone" from "not found"; check ShareLink.Active().
func (s *Store) GetShareLinkByPrefix(ctx context.Context, prefix string) (link ShareLink, found bool, err error) {
	const q = `SELECT id, tenant_id, queue_id, message_id, token_prefix, token_hash, label,
	                  COALESCE(created_by::text, ''), expires_at, revoked_at, last_viewed_at, view_count, created_at
	           FROM message_share_links
	           WHERE token_prefix = $1
	           LIMIT 1`
	err = s.Pool.QueryRow(ctx, q, prefix).Scan(
		&link.ID, &link.TenantID, &link.QueueID, &link.MessageID, &link.TokenPrefix, &link.TokenHash,
		&link.Label, &link.CreatedBy, &link.ExpiresAt, &link.RevokedAt, &link.LastViewedAt, &link.ViewCount, &link.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ShareLink{}, false, nil
	}
	if err != nil {
		return ShareLink{}, false, fmt.Errorf("get share link by prefix: %w", err)
	}
	return link, true, nil
}

// ListShareLinks returns a tenant's share links for a given queue id, newest first.
func (s *Store) ListShareLinks(ctx context.Context, tenantID, queueID string) ([]ShareLink, error) {
	const q = `SELECT id, tenant_id, queue_id, message_id, token_prefix, token_hash, label,
	                  COALESCE(created_by::text, ''), expires_at, revoked_at, last_viewed_at, view_count, created_at
	           FROM message_share_links
	           WHERE tenant_id = $1 AND queue_id = $2
	           ORDER BY created_at DESC`
	rows, err := s.Pool.Query(ctx, q, tenantID, queueID)
	if err != nil {
		return nil, fmt.Errorf("list share links: %w", err)
	}
	defer rows.Close()

	var out []ShareLink
	for rows.Next() {
		var l ShareLink
		if err := rows.Scan(
			&l.ID, &l.TenantID, &l.QueueID, &l.MessageID, &l.TokenPrefix, &l.TokenHash,
			&l.Label, &l.CreatedBy, &l.ExpiresAt, &l.RevokedAt, &l.LastViewedAt, &l.ViewCount, &l.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan share link: %w", err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// RevokeShareLink marks a link revoked (scoped to tenant). Returns false if no matching,
// not-already-revoked link was found.
func (s *Store) RevokeShareLink(ctx context.Context, tenantID, id string) (bool, error) {
	const q = `UPDATE message_share_links
	           SET revoked_at = now()
	           WHERE id = $1 AND tenant_id = $2 AND revoked_at IS NULL`
	tag, err := s.Pool.Exec(ctx, q, id, tenantID)
	if err != nil {
		return false, fmt.Errorf("revoke share link: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// TouchShareLink records a successful public view (increments view_count, sets last_viewed_at).
// Best-effort: callers may ignore the error since a failed audit bump must not fail the view.
func (s *Store) TouchShareLink(ctx context.Context, id string) error {
	const q = `UPDATE message_share_links
	           SET view_count = view_count + 1, last_viewed_at = now()
	           WHERE id = $1`
	if _, err := s.Pool.Exec(ctx, q, id); err != nil {
		return fmt.Errorf("touch share link: %w", err)
	}
	return nil
}
