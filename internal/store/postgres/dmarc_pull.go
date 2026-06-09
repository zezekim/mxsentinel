package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// GetDmarcPullCursor returns the last-synced date_end for a tenant.
// Returns the zero time when no cursor exists yet (first run).
func (s *Store) GetDmarcPullCursor(ctx context.Context, tenantID string) (time.Time, error) {
	const q = `SELECT last_date_end FROM dmarc_pull_cursors WHERE tenant_id = $1`
	var t time.Time
	err := s.Pool.QueryRow(ctx, q, tenantID).Scan(&t)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("get dmarc pull cursor: %w", err)
	}
	return t, nil
}

// SetDmarcPullCursor upserts the last-synced date_end for a tenant.
func (s *Store) SetDmarcPullCursor(ctx context.Context, tenantID string, lastDateEnd time.Time) error {
	const q = `INSERT INTO dmarc_pull_cursors (tenant_id, last_date_end, updated_at)
	           VALUES ($1, $2, now())
	           ON CONFLICT (tenant_id) DO UPDATE
	             SET last_date_end = EXCLUDED.last_date_end,
	                 updated_at    = now()`
	if _, err := s.Pool.Exec(ctx, q, tenantID, lastDateEnd); err != nil {
		return fmt.Errorf("set dmarc pull cursor: %w", err)
	}
	return nil
}
