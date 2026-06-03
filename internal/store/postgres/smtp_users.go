package postgres

import (
	"context"
	"fmt"
)

// SMTPUser is a projection of the smtp_users table. PasswordHash is never read back
// (it is only ever written); the relay's Dovecot passdb reads it directly from Postgres.
type SMTPUser struct {
	ID        string
	TenantID  string
	Username  string
	Domain    string
	Enabled   bool
	CreatedAt string
}

// CreateSMTPUser inserts an SMTP submission credential and returns its id. username must
// be globally unique (Dovecot looks it up across all tenants); a duplicate returns an
// error. passwordHash is a bcrypt hash. domain is optional ("" stores NULL).
func (s *Store) CreateSMTPUser(ctx context.Context, tenantID, username, passwordHash, domain string) (string, error) {
	const q = `INSERT INTO smtp_users (tenant_id, username, password_hash, domain)
	           VALUES ($1, $2, $3, NULLIF($4, ''))
	           RETURNING id`
	var id string
	if err := s.Pool.QueryRow(ctx, q, tenantID, username, passwordHash, domain).Scan(&id); err != nil {
		return "", fmt.Errorf("create smtp user: %w", err)
	}
	return id, nil
}

// ListSMTPUsers returns a tenant's SMTP submission users (no password hashes).
func (s *Store) ListSMTPUsers(ctx context.Context, tenantID string) ([]SMTPUser, error) {
	const q = `SELECT id, tenant_id, username::text, COALESCE(domain, ''), enabled, created_at::text
	           FROM smtp_users WHERE tenant_id = $1 ORDER BY username`
	rows, err := s.Pool.Query(ctx, q, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list smtp users: %w", err)
	}
	defer rows.Close()

	var out []SMTPUser
	for rows.Next() {
		var u SMTPUser
		if err := rows.Scan(&u.ID, &u.TenantID, &u.Username, &u.Domain, &u.Enabled, &u.CreatedAt); err != nil {
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
func (s *Store) UpdateSMTPUserPassword(ctx context.Context, tenantID, id, passwordHash string) (bool, error) {
	const q = `UPDATE smtp_users SET password_hash = $3, updated_at = now()
	           WHERE id = $2 AND tenant_id = $1`
	tag, err := s.Pool.Exec(ctx, q, tenantID, id, passwordHash)
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
