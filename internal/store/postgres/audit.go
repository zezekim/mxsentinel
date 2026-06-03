package postgres

import (
	"context"
	"fmt"
	"time"
)

// AuditEvent is a minimal projection of the audit_events table.
type AuditEvent struct {
	ID           string
	TenantID     string
	CredentialID *string // nil when no credential recorded
	Method       string
	Path         string
	Status       int
	CreatedAt    time.Time
}

// InsertAuditEvent records one mutating request. credentialID "" stores NULL.
func (s *Store) InsertAuditEvent(ctx context.Context, tenantID, credentialID, method, path string, status int) error {
	const q = `INSERT INTO audit_events (tenant_id, credential_id, method, path, status)
	           VALUES ($1, NULLIF($2,'')::uuid, $3, $4, $5)`
	_, err := s.Pool.Exec(ctx, q, tenantID, credentialID, method, path, status)
	if err != nil {
		return fmt.Errorf("insert audit event: %w", err)
	}
	return nil
}

// ListAuditEvents returns a tenant's audit events newest-first (limit default 50, cap 500).
func (s *Store) ListAuditEvents(ctx context.Context, tenantID string, limit int) ([]AuditEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}

	const q = `SELECT id, tenant_id, credential_id, method, path, status, created_at
	           FROM audit_events WHERE tenant_id = $1 ORDER BY created_at DESC LIMIT $2`
	rows, err := s.Pool.Query(ctx, q, tenantID, limit)
	if err != nil {
		return nil, fmt.Errorf("list audit events: %w", err)
	}
	defer rows.Close()

	var out []AuditEvent
	for rows.Next() {
		var e AuditEvent
		if err := rows.Scan(&e.ID, &e.TenantID, &e.CredentialID, &e.Method, &e.Path, &e.Status, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan audit event: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
