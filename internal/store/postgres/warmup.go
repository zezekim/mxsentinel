package postgres

import (
	"context"
	"fmt"
	"time"
)

// WarmupPlan represents an IP warm-up plan for a tenant.
type WarmupPlan struct {
	ID                string
	TenantID          string
	Name              string
	IPPool            string
	TargetDailyVolume int
	CurrentDay        int
	Stage             string // ramping, established, paused
	StartedAt         time.Time
	CompletedAt       *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// WarmupDayStat holds per-day delivery statistics for a warm-up plan.
type WarmupDayStat struct {
	ID             string
	PlanID         string
	DayNumber      int
	StatDate       time.Time
	Sent           int
	Delivered      int
	Bounced        int
	Deferred       int
	AcceptanceRate *float64
	RecordedAt     time.Time
}

// ListWarmupPlans returns all warm-up plans for the given tenant.
func (s *Store) ListWarmupPlans(ctx context.Context, tenantID string) ([]WarmupPlan, error) {
	const q = `
		SELECT id, tenant_id, name, ip_pool, target_daily_volume, current_day,
		       stage, started_at, completed_at, created_at, updated_at
		FROM warmup_plans
		WHERE tenant_id = $1
		ORDER BY created_at DESC`

	rows, err := s.Pool.Query(ctx, q, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list warmup plans: %w", err)
	}
	defer rows.Close()

	var plans []WarmupPlan
	for rows.Next() {
		var p WarmupPlan
		if err := rows.Scan(
			&p.ID, &p.TenantID, &p.Name, &p.IPPool, &p.TargetDailyVolume,
			&p.CurrentDay, &p.Stage, &p.StartedAt, &p.CompletedAt,
			&p.CreatedAt, &p.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan warmup plan: %w", err)
		}
		plans = append(plans, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list warmup plans rows: %w", err)
	}
	return plans, nil
}

// CreateWarmupPlan inserts a new warm-up plan and returns its generated ID.
func (s *Store) CreateWarmupPlan(ctx context.Context, tenantID, name, ipPool string, targetDailyVolume int) (string, error) {
	const q = `
		INSERT INTO warmup_plans
			(tenant_id, name, ip_pool, target_daily_volume, current_day, stage, started_at, created_at, updated_at)
		VALUES
			($1, $2, $3, $4, 0, 'ramping', NOW(), NOW(), NOW())
		RETURNING id`

	var id string
	if err := s.Pool.QueryRow(ctx, q, tenantID, name, ipPool, targetDailyVolume).Scan(&id); err != nil {
		return "", fmt.Errorf("create warmup plan: %w", err)
	}
	return id, nil
}

// GetWarmupPlan fetches a single warm-up plan by tenant and plan ID.
func (s *Store) GetWarmupPlan(ctx context.Context, tenantID, id string) (WarmupPlan, error) {
	const q = `
		SELECT id, tenant_id, name, ip_pool, target_daily_volume, current_day,
		       stage, started_at, completed_at, created_at, updated_at
		FROM warmup_plans
		WHERE tenant_id = $1 AND id = $2`

	var p WarmupPlan
	if err := s.Pool.QueryRow(ctx, q, tenantID, id).Scan(
		&p.ID, &p.TenantID, &p.Name, &p.IPPool, &p.TargetDailyVolume,
		&p.CurrentDay, &p.Stage, &p.StartedAt, &p.CompletedAt,
		&p.CreatedAt, &p.UpdatedAt,
	); err != nil {
		return WarmupPlan{}, fmt.Errorf("get warmup plan: %w", err)
	}
	return p, nil
}

// UpdateWarmupStage updates the stage of a warm-up plan. When stage is
// "established" the completed_at timestamp is also set. Returns true if a row
// was affected.
func (s *Store) UpdateWarmupStage(ctx context.Context, tenantID, id, stage string) (bool, error) {
	var q string
	if stage == "established" {
		q = `UPDATE warmup_plans
			 SET stage = $1, completed_at = NOW(), updated_at = NOW()
			 WHERE tenant_id = $2 AND id = $3`
	} else {
		q = `UPDATE warmup_plans
			 SET stage = $1, updated_at = NOW()
			 WHERE tenant_id = $2 AND id = $3`
	}
	tag, err := s.Pool.Exec(ctx, q, stage, tenantID, id)
	if err != nil {
		return false, fmt.Errorf("update warmup stage: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// DeleteWarmupPlan removes a warm-up plan (and cascades to day stats if the
// schema uses ON DELETE CASCADE). Returns true if a row was deleted.
func (s *Store) DeleteWarmupPlan(ctx context.Context, tenantID, id string) (bool, error) {
	const q = `DELETE FROM warmup_plans WHERE tenant_id = $1 AND id = $2`

	tag, err := s.Pool.Exec(ctx, q, tenantID, id)
	if err != nil {
		return false, fmt.Errorf("delete warmup plan: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ListWarmupDayStats returns all day-level statistics for the given plan,
// ordered by day number ascending.
func (s *Store) ListWarmupDayStats(ctx context.Context, planID string) ([]WarmupDayStat, error) {
	const q = `
		SELECT id, plan_id, day_number, stat_date, sent, delivered, bounced,
		       deferred, acceptance_rate, recorded_at
		FROM warmup_day_stats
		WHERE plan_id = $1
		ORDER BY day_number ASC`

	rows, err := s.Pool.Query(ctx, q, planID)
	if err != nil {
		return nil, fmt.Errorf("list warmup day stats: %w", err)
	}
	defer rows.Close()

	var stats []WarmupDayStat
	for rows.Next() {
		var d WarmupDayStat
		if err := rows.Scan(
			&d.ID, &d.PlanID, &d.DayNumber, &d.StatDate,
			&d.Sent, &d.Delivered, &d.Bounced, &d.Deferred,
			&d.AcceptanceRate, &d.RecordedAt,
		); err != nil {
			return nil, fmt.Errorf("scan warmup day stat: %w", err)
		}
		stats = append(stats, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list warmup day stats rows: %w", err)
	}
	return stats, nil
}

// UpsertWarmupDayStat inserts or updates the delivery statistics for a
// specific day within a warm-up plan. The stat_date is set to the current
// calendar date (UTC) when inserting; on conflict the counters and
// acceptance_rate are overwritten and recorded_at is refreshed.
func (s *Store) UpsertWarmupDayStat(
	ctx context.Context,
	planID string,
	dayNumber, sent, delivered, bounced, deferred int,
	acceptanceRate *float64,
) error {
	const q = `
		INSERT INTO warmup_day_stats
			(plan_id, day_number, stat_date, sent, delivered, bounced, deferred, acceptance_rate, recorded_at)
		VALUES
			($1, $2, CURRENT_DATE, $3, $4, $5, $6, $7, NOW())
		ON CONFLICT (plan_id, day_number) DO UPDATE SET
			sent            = EXCLUDED.sent,
			delivered       = EXCLUDED.delivered,
			bounced         = EXCLUDED.bounced,
			deferred        = EXCLUDED.deferred,
			acceptance_rate = EXCLUDED.acceptance_rate,
			recorded_at     = NOW()`

	if _, err := s.Pool.Exec(ctx, q,
		planID, dayNumber, sent, delivered, bounced, deferred, acceptanceRate,
	); err != nil {
		return fmt.Errorf("upsert warmup day stat: %w", err)
	}
	return nil
}
