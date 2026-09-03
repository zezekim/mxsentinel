package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// SMTPUser is a projection of the smtp_users table. PasswordHash is never read back
// (it is only ever written); the relay's Dovecot passdb reads it directly from Postgres.
// HasWebmail reports whether a sealed password copy exists (password_enc IS NOT NULL), i.e.
// whether webmail autologin can be offered for this user — the ciphertext itself is only
// read by GetSMTPUserWebmailCredential at redeem time.
type SMTPUser struct {
	ID         string
	TenantID   string
	Username   string
	Domain     string
	Enabled    bool
	HasWebmail bool
	CreatedAt  string
}

// CreateSMTPUser inserts an SMTP submission credential and returns its id. username must
// be globally unique (Dovecot looks it up across all tenants); a duplicate returns an
// error. passwordHash is a bcrypt hash. domain is optional ("" stores NULL). passwordEnc is
// the AES-256-GCM sealed copy of the same password used for webmail autologin; pass "" to
// store NULL (no encryption key configured → no reversible password at rest, and webmail
// autologin is unavailable for this user).
func (s *Store) CreateSMTPUser(ctx context.Context, tenantID, username, passwordHash, domain, passwordEnc string) (string, error) {
	const q = `INSERT INTO smtp_users (tenant_id, username, password_hash, domain, password_enc)
	           VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''))
	           RETURNING id`
	var id string
	if err := s.Pool.QueryRow(ctx, q, tenantID, username, passwordHash, domain, passwordEnc).Scan(&id); err != nil {
		return "", fmt.Errorf("create smtp user: %w", err)
	}
	return id, nil
}

// ListSMTPUsers returns a tenant's SMTP submission users (no password hashes).
func (s *Store) ListSMTPUsers(ctx context.Context, tenantID string) ([]SMTPUser, error) {
	const q = `SELECT id, tenant_id, username::text, COALESCE(domain, ''), enabled,
	                  password_enc IS NOT NULL, created_at::text
	           FROM smtp_users WHERE tenant_id = $1 ORDER BY username`
	rows, err := s.Pool.Query(ctx, q, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list smtp users: %w", err)
	}
	defer rows.Close()

	var out []SMTPUser
	for rows.Next() {
		var u SMTPUser
		if err := rows.Scan(&u.ID, &u.TenantID, &u.Username, &u.Domain, &u.Enabled, &u.HasWebmail, &u.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan smtp user: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// SetSMTPUserEnabled enables or disables an SMTP user. The tenant_id predicate enforces
// tenant isolation. found=false (nil error) when no such row belongs to the tenant.
func (s *Store) SetSMTPUserEnabled(ctx context.Context, tenantID, id string, enabled bool) (bool, error) {
	const q = `UPDATE smtp_users SET enabled = $3, updated_at = now()
	           WHERE id = $2 AND tenant_id = $1`
	tag, err := s.Pool.Exec(ctx, q, tenantID, id, enabled)
	if err != nil {
		return false, fmt.Errorf("set smtp user enabled: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// UpdateSMTPUserPassword resets an SMTP user's password (bcrypt hash). Tenant-scoped.
// passwordEnc is the sealed copy for webmail autologin; "" clears it (NULL), which is what
// we want when no encryption key is configured — a stale ciphertext would otherwise unseal
// to the OLD password and hand Roundcube a credential the relay no longer accepts.
func (s *Store) UpdateSMTPUserPassword(ctx context.Context, tenantID, id, passwordHash, passwordEnc string) (bool, error) {
	const q = `UPDATE smtp_users SET password_hash = $3, password_enc = NULLIF($4, ''), updated_at = now()
	           WHERE id = $2 AND tenant_id = $1`
	tag, err := s.Pool.Exec(ctx, q, tenantID, id, passwordHash, passwordEnc)
	if err != nil {
		return false, fmt.Errorf("update smtp user password: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// DeleteSMTPUser removes an SMTP user by id. Tenant-scoped.
func (s *Store) DeleteSMTPUser(ctx context.Context, tenantID, id string) (bool, error) {
	const q = `DELETE FROM smtp_users WHERE id = $2 AND tenant_id = $1`
	tag, err := s.Pool.Exec(ctx, q, tenantID, id)
	if err != nil {
		return false, fmt.Errorf("delete smtp user: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// DisableSMTPUserByUsername atomically disables an SMTP user iff it is currently enabled,
// returning the owning tenant id and suspended=true only on that TRUE->FALSE transition.
// This makes auto-suspension idempotent: a repeat call (or a daemon restart) against an
// already-disabled user returns suspended=false without re-acting, so we don't reopen
// incidents. Username is globally unique, so no tenant scoping is needed here.
func (s *Store) DisableSMTPUserByUsername(ctx context.Context, username string) (tenantID string, suspended bool, err error) {
	const q = `UPDATE smtp_users SET enabled = FALSE, updated_at = now()
	           WHERE username = $1 AND enabled = TRUE
	           RETURNING tenant_id`
	err = s.Pool.QueryRow(ctx, q, username).Scan(&tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil // no such user, or already disabled
	}
	if err != nil {
		return "", false, fmt.Errorf("disable smtp user by username: %w", err)
	}
	return tenantID, true, nil
}

// DeleteSMTPUserByUsername removes an SMTP user by username (used by the mxctl CLI).
// Tenant-scoped so a tenant can only delete its own credentials.
func (s *Store) DeleteSMTPUserByUsername(ctx context.Context, tenantID, username string) (bool, error) {
	const q = `DELETE FROM smtp_users WHERE username = $2 AND tenant_id = $1`
	tag, err := s.Pool.Exec(ctx, q, tenantID, username)
	if err != nil {
		return false, fmt.Errorf("delete smtp user by username: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}
