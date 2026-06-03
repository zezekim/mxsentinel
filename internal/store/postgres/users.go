package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// User is a projection of the users table. PasswordHash is only populated by
// GetUserByEmail (for login); list/other reads leave it empty.
type User struct {
	ID           string
	TenantID     string
	Email        string
	Role         string
	Status       string
	PasswordHash string
}

// CreateUser inserts an active user with a bcrypt password hash and returns its id.
func (s *Store) CreateUser(ctx context.Context, tenantID, email, passwordHash, role string) (string, error) {
	const q = `INSERT INTO users (tenant_id, email, role, status, password_hash)
	           VALUES ($1, $2, $3::user_role, 'active', $4)
	           RETURNING id`
	var id string
	if err := s.Pool.QueryRow(ctx, q, tenantID, email, role, passwordHash).Scan(&id); err != nil {
		return "", fmt.Errorf("create user: %w", err)
	}
	return id, nil
}

// GetUserByEmail looks up a user by email (across tenants) for login. found=false (nil
// error) when no such user exists.
func (s *Store) GetUserByEmail(ctx context.Context, email string) (User, bool, error) {
	const q = `SELECT id, tenant_id, email::text, role::text, status::text, COALESCE(password_hash, '')
	           FROM users WHERE email = $1 LIMIT 1`
	var u User
	err := s.Pool.QueryRow(ctx, q, email).Scan(&u.ID, &u.TenantID, &u.Email, &u.Role, &u.Status, &u.PasswordHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, fmt.Errorf("get user by email: %w", err)
	}
	return u, true, nil
}

// GetUserByID looks up a user by id, including the password hash (for self-service
// password changes). found=false (nil error) when no such user exists.
func (s *Store) GetUserByID(ctx context.Context, userID string) (User, bool, error) {
	const q = `SELECT id, tenant_id, email::text, role::text, status::text, COALESCE(password_hash, '')
	           FROM users WHERE id = $1`
	var u User
	err := s.Pool.QueryRow(ctx, q, userID).Scan(&u.ID, &u.TenantID, &u.Email, &u.Role, &u.Status, &u.PasswordHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, fmt.Errorf("get user by id: %w", err)
	}
	return u, true, nil
}

// UpdateUserPassword sets a user's bcrypt password hash. Scoped by tenant for defense in
// depth. found=false (nil error) when no matching user exists.
func (s *Store) UpdateUserPassword(ctx context.Context, userID, tenantID, passwordHash string) (bool, error) {
	const q = `UPDATE users SET password_hash = $3, updated_at = now()
	           WHERE id = $1 AND tenant_id = $2`
	tag, err := s.Pool.Exec(ctx, q, userID, tenantID, passwordHash)
	if err != nil {
		return false, fmt.Errorf("update user password: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// UpdateUserPasswordByEmail sets a user's bcrypt password hash, looked up by tenant +
// email (used by `mxctl user set-password` for out-of-band resets). found=false (nil
// error) when no matching user exists.
func (s *Store) UpdateUserPasswordByEmail(ctx context.Context, tenantID, email, passwordHash string) (bool, error) {
	const q = `UPDATE users SET password_hash = $3, updated_at = now()
	           WHERE tenant_id = $1 AND email = $2`
	tag, err := s.Pool.Exec(ctx, q, tenantID, email, passwordHash)
	if err != nil {
		return false, fmt.Errorf("update user password by email: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ListUsers returns a tenant's users (without password hashes).
func (s *Store) ListUsers(ctx context.Context, tenantID string) ([]User, error) {
	const q = `SELECT id, tenant_id, email::text, role::text, status::text
	           FROM users WHERE tenant_id = $1 ORDER BY email`
	rows, err := s.Pool.Query(ctx, q, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.TenantID, &u.Email, &u.Role, &u.Status); err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
