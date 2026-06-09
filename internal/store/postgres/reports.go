package postgres

import (
	"context"
	"fmt"
	"time"
)

// ReportSchedule represents a scheduled report configuration for a tenant.
type ReportSchedule struct {
	ID                string
	TenantID          string
	Name              string
	Frequency         string // daily, weekly, monthly
	Recipients        []string
	IncludeDNS        bool
	IncludeDMARC      bool
	IncludeIncidents  bool
	IncludeReputation bool
	Enabled           bool
	LastSentAt        *time.Time
	NextRunAt         *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// ListReportSchedules returns all report schedules for the given tenant.
func (s *Store) ListReportSchedules(ctx context.Context, tenantID string) ([]ReportSchedule, error) {
	const q = `
		SELECT id, tenant_id, name, frequency, recipients,
		       include_dns, include_dmarc, include_incidents, include_reputation,
		       enabled, last_sent_at, next_run_at, created_at, updated_at
		FROM report_schedules
		WHERE tenant_id = $1
		ORDER BY created_at DESC`

	rows, err := s.Pool.Query(ctx, q, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list report schedules: %w", err)
	}
	defer rows.Close()

	var schedules []ReportSchedule
	for rows.Next() {
		var rs ReportSchedule
		if err := rows.Scan(
			&rs.ID, &rs.TenantID, &rs.Name, &rs.Frequency, &rs.Recipients,
			&rs.IncludeDNS, &rs.IncludeDMARC, &rs.IncludeIncidents, &rs.IncludeReputation,
			&rs.Enabled, &rs.LastSentAt, &rs.NextRunAt, &rs.CreatedAt, &rs.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan report schedule: %w", err)
		}
		schedules = append(schedules, rs)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list report schedules rows: %w", err)
	}
	return schedules, nil
}

// CreateReportSchedule inserts a new report schedule and returns its generated ID.
func (s *Store) CreateReportSchedule(
	ctx context.Context,
	tenantID, name, frequency string,
	recipients []string,
	includeDNS, includeDMARC, includeIncidents, includeReputation bool,
) (string, error) {
	const q = `
		INSERT INTO report_schedules
			(tenant_id, name, frequency, recipients,
			 include_dns, include_dmarc, include_incidents, include_reputation,
			 enabled, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, TRUE, NOW(), NOW())
		RETURNING id`

	var id string
	err := s.Pool.QueryRow(ctx, q,
		tenantID, name, frequency, recipients,
		includeDNS, includeDMARC, includeIncidents, includeReputation,
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("create report schedule: %w", err)
	}
	return id, nil
}

// UpdateReportSchedule updates an existing report schedule owned by tenantID.
// Returns true if a row was updated, false if not found.
func (s *Store) UpdateReportSchedule(
	ctx context.Context,
	tenantID, id, frequency string,
	recipients []string,
	includeDNS, includeDMARC, includeIncidents, includeReputation, enabled bool,
) (bool, error) {
	const q = `
		UPDATE report_schedules
		SET frequency           = $3,
		    recipients          = $4,
		    include_dns         = $5,
		    include_dmarc       = $6,
		    include_incidents   = $7,
		    include_reputation  = $8,
		    enabled             = $9,
		    updated_at          = NOW()
		WHERE tenant_id = $1 AND id = $2`

	tag, err := s.Pool.Exec(ctx, q,
		tenantID, id, frequency, recipients,
		includeDNS, includeDMARC, includeIncidents, includeReputation, enabled,
	)
	if err != nil {
		return false, fmt.Errorf("update report schedule: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// DeleteReportSchedule removes a report schedule owned by tenantID.
// Returns true if a row was deleted, false if not found.
func (s *Store) DeleteReportSchedule(ctx context.Context, tenantID, id string) (bool, error) {
	const q = `DELETE FROM report_schedules WHERE tenant_id = $1 AND id = $2`

	tag, err := s.Pool.Exec(ctx, q, tenantID, id)
	if err != nil {
		return false, fmt.Errorf("delete report schedule: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// GetDueReportSchedules returns all enabled schedules whose next_run_at is in
// the past or has not been set yet (NULL).
func (s *Store) GetDueReportSchedules(ctx context.Context) ([]ReportSchedule, error) {
	const q = `
		SELECT id, tenant_id, name, frequency, recipients,
		       include_dns, include_dmarc, include_incidents, include_reputation,
		       enabled, last_sent_at, next_run_at, created_at, updated_at
		FROM report_schedules
		WHERE enabled = TRUE
		  AND (next_run_at IS NULL OR next_run_at <= NOW())
		ORDER BY next_run_at ASC NULLS FIRST`

	rows, err := s.Pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("get due report schedules: %w", err)
	}
	defer rows.Close()

	var schedules []ReportSchedule
	for rows.Next() {
		var rs ReportSchedule
		if err := rows.Scan(
			&rs.ID, &rs.TenantID, &rs.Name, &rs.Frequency, &rs.Recipients,
			&rs.IncludeDNS, &rs.IncludeDMARC, &rs.IncludeIncidents, &rs.IncludeReputation,
			&rs.Enabled, &rs.LastSentAt, &rs.NextRunAt, &rs.CreatedAt, &rs.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan due report schedule: %w", err)
		}
		schedules = append(schedules, rs)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get due report schedules rows: %w", err)
	}
	return schedules, nil
}

// MarkReportSent sets last_sent_at to now and advances next_run_at for the
// given schedule ID.
func (s *Store) MarkReportSent(ctx context.Context, id string, nextRunAt time.Time) error {
	const q = `
		UPDATE report_schedules
		SET last_sent_at = NOW(),
		    next_run_at  = $2,
		    updated_at   = NOW()
		WHERE id = $1`

	_, err := s.Pool.Exec(ctx, q, id, nextRunAt)
	if err != nil {
		return fmt.Errorf("mark report sent: %w", err)
	}
	return nil
}
