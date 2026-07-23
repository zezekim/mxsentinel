package postgres

import (
	"context"
	"fmt"
	"time"
)

// SuppressionEntry is one row of a tenant's suppression list. RecipientHash is the keyed
// HMAC-SHA256 of the recipient address produced at the telemetry parser boundary — the full
// address is never stored.
type SuppressionEntry struct {
	ID            string
	TenantID      string
	RecipientHash string
	Reason        string
	Category      string
	Source        string
	CreatedAt     time.Time
	ExpiresAt     *time.Time
}

// ListSuppression returns a tenant's suppression entries, newest first. When includeExpired
// is false, entries whose expires_at has passed are omitted (permanent entries — NULL
// expires_at — are always included). limit is capped defensively.
func (s *Store) ListSuppression(ctx context.Context, tenantID string, includeExpired bool, limit int) ([]SuppressionEntry, error) {
	if limit <= 0 || limit > 5000 {
		limit = 1000
	}
	q := `
		SELECT id, tenant_id, recipient_hash, reason, category, source, created_at, expires_at
		FROM suppression_entries
		WHERE tenant_id = $1`
	if !includeExpired {
		q += ` AND (expires_at IS NULL OR expires_at > now())`
	}
	q += ` ORDER BY created_at DESC LIMIT $2`

	rows, err := s.Pool.Query(ctx, q, tenantID, limit)
	if err != nil {
		return nil, fmt.Errorf("list suppression: %w", err)
	}
	defer rows.Close()

	var out []SuppressionEntry
	for rows.Next() {
		var e SuppressionEntry
		if err := rows.Scan(
			&e.ID, &e.TenantID, &e.RecipientHash, &e.Reason,
			&e.Category, &e.Source, &e.CreatedAt, &e.ExpiresAt,
		); err != nil {
			return nil, fmt.Errorf("scan suppression: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ActiveSuppressionHashes returns all non-expired entries for a tenant as export records.
// Used to build a relay-sync artifact. Ordered by hash for a stable diff.
func (s *Store) ActiveSuppressionHashes(ctx context.Context, tenantID string) ([]SuppressionEntry, error) {
	const q = `
		SELECT id, tenant_id, recipient_hash, reason, category, source, created_at, expires_at
		FROM suppression_entries
		WHERE tenant_id = $1 AND (expires_at IS NULL OR expires_at > now())
		ORDER BY recipient_hash`
	rows, err := s.Pool.Query(ctx, q, tenantID)
	if err != nil {
		return nil, fmt.Errorf("active suppression: %w", err)
	}
	defer rows.Close()

	var out []SuppressionEntry
	for rows.Next() {
		var e SuppressionEntry
		if err := rows.Scan(
			&e.ID, &e.TenantID, &e.RecipientHash, &e.Reason,
			&e.Category, &e.Source, &e.CreatedAt, &e.ExpiresAt,
		); err != nil {
			return nil, fmt.Errorf("scan active suppression: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// UpsertSuppression inserts or refreshes a suppression entry keyed on
// (tenant_id, recipient_hash). On conflict it updates reason/category/source/expires_at but
// keeps the original created_at, so the first-seen time is preserved while the reason can be
// upgraded (e.g. spam_block -> hard_bounce). Returns true if a new row was inserted.
func (s *Store) UpsertSuppression(
	ctx context.Context,
	tenantID, recipientHash, reason, category, source string,
	expiresAt *time.Time,
) (bool, error) {
	const q = `
		INSERT INTO suppression_entries
			(tenant_id, recipient_hash, reason, category, source, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (tenant_id, recipient_hash) DO UPDATE SET
			reason     = EXCLUDED.reason,
			category   = EXCLUDED.category,
			source     = EXCLUDED.source,
			expires_at = EXCLUDED.expires_at
		RETURNING (xmax = 0) AS inserted`
	var inserted bool
	if err := s.Pool.QueryRow(ctx, q, tenantID, recipientHash, reason, category, source, expiresAt).Scan(&inserted); err != nil {
		return false, fmt.Errorf("upsert suppression: %w", err)
	}
	return inserted, nil
}

// DeleteSuppression removes a suppression entry by recipient hash. Returns true if a row was
// deleted (i.e. the recipient is no longer suppressed).
func (s *Store) DeleteSuppression(ctx context.Context, tenantID, recipientHash string) (bool, error) {
	const q = `DELETE FROM suppression_entries WHERE tenant_id = $1 AND recipient_hash = $2`
	tag, err := s.Pool.Exec(ctx, q, tenantID, recipientHash)
	if err != nil {
		return false, fmt.Errorf("delete suppression: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// BounceRollupRow is a per-day, per-domain, per-category classified bounce count.
type BounceRollupRow struct {
	TenantID   string
	Day        time.Time
	FromDomain string
	Category   string
	Count      int
}

// UpsertBounceRollup writes an authoritative classified-bounce count for a
// (tenant, day, from_domain, category) cell. The bounced daemon recomputes these over a
// fixed lookback window each pass, so the count REPLACES any prior value (idempotent).
func (s *Store) UpsertBounceRollup(ctx context.Context, r BounceRollupRow) error {
	const q = `
		INSERT INTO bounce_rollup (tenant_id, day, from_domain, category, count, updated_at)
		VALUES ($1, $2, $3, $4, $5, now())
		ON CONFLICT (tenant_id, day, from_domain, category) DO UPDATE SET
			count      = EXCLUDED.count,
			updated_at = now()`
	if _, err := s.Pool.Exec(ctx, q, r.TenantID, r.Day, r.FromDomain, r.Category, r.Count); err != nil {
		return fmt.Errorf("upsert bounce rollup: %w", err)
	}
	return nil
}

// CategoryCount is a classified-bounce total for one category.
type CategoryCount struct {
	Category string
	Count    int
}

// BounceCategoryTotals returns per-category classified-bounce totals for a tenant since a
// given time, from the bounce_rollup table (populated by the bounced daemon).
func (s *Store) BounceCategoryTotals(ctx context.Context, tenantID string, since time.Time) ([]CategoryCount, error) {
	const q = `
		SELECT category, SUM(count)::bigint
		FROM bounce_rollup
		WHERE tenant_id = $1 AND day >= $2::date
		GROUP BY category
		ORDER BY SUM(count) DESC`
	rows, err := s.Pool.Query(ctx, q, tenantID, since)
	if err != nil {
		return nil, fmt.Errorf("bounce category totals: %w", err)
	}
	defer rows.Close()

	var out []CategoryCount
	for rows.Next() {
		var c CategoryCount
		var n int64
		if err := rows.Scan(&c.Category, &n); err != nil {
			return nil, fmt.Errorf("scan bounce category total: %w", err)
		}
		c.Count = int(n)
		out = append(out, c)
	}
	return out, rows.Err()
}
