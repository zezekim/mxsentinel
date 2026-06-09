package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// AlertRule represents a row in the alert_rules table.
type AlertRule struct {
	ID         string
	TenantID   string
	Name       string
	Signal     string
	Condition  []byte // JSONB raw
	ChannelIDs []string
	Enabled    bool
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// NotificationChannel represents a row in the notification_channels table.
type NotificationChannel struct {
	ID        string
	TenantID  string
	Kind      string
	Name      string
	Config    []byte // JSONB raw
	Enabled   bool
	CreatedAt time.Time
}

// AlertEvent represents a row in the alert_events table.
type AlertEvent struct {
	ID          string
	TenantID    string
	RuleID      string
	State       string
	TriggeredAt time.Time
	ResolvedAt  *time.Time
	Payload     []byte
	CreatedAt   time.Time
}

// ListAlertRules returns all alert rules for the given tenant.
func (s *Store) ListAlertRules(ctx context.Context, tenantID string) ([]AlertRule, error) {
	const q = `
		SELECT id, tenant_id, name, signal::text, condition, channel_ids, enabled, created_at, updated_at
		FROM alert_rules
		WHERE tenant_id = $1
		ORDER BY created_at DESC`

	rows, err := s.Pool.Query(ctx, q, tenantID)
	if err != nil {
		return nil, fmt.Errorf("ListAlertRules: %w", err)
	}
	defer rows.Close()

	var rules []AlertRule
	for rows.Next() {
		var r AlertRule
		if err := rows.Scan(
			&r.ID, &r.TenantID, &r.Name, &r.Signal,
			&r.Condition, &r.ChannelIDs, &r.Enabled,
			&r.CreatedAt, &r.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("ListAlertRules scan: %w", err)
		}
		rules = append(rules, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ListAlertRules rows: %w", err)
	}
	return rules, nil
}

// CreateAlertRule inserts a new alert rule and returns its generated ID.
func (s *Store) CreateAlertRule(ctx context.Context, tenantID, name, signal string, condition []byte, channelIDs []string) (string, error) {
	const q = `
		INSERT INTO alert_rules (tenant_id, name, signal, condition, channel_ids, enabled, created_at, updated_at)
		VALUES ($1, $2, $3::text, $4, $5, true, now(), now())
		RETURNING id`

	var id string
	err := s.Pool.QueryRow(ctx, q, tenantID, name, signal, condition, channelIDs).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("CreateAlertRule: %w", err)
	}
	return id, nil
}

// UpdateAlertRule updates the enabled flag, condition, and channel_ids for a rule.
// Returns true if a row was found and updated.
func (s *Store) UpdateAlertRule(ctx context.Context, tenantID, id string, enabled bool, condition []byte, channelIDs []string) (bool, error) {
	const q = `
		UPDATE alert_rules
		SET enabled = $1, condition = $2, channel_ids = $3, updated_at = now()
		WHERE id = $4 AND tenant_id = $5`

	tag, err := s.Pool.Exec(ctx, q, enabled, condition, channelIDs, id, tenantID)
	if err != nil {
		return false, fmt.Errorf("UpdateAlertRule: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// DeleteAlertRule removes an alert rule. Returns true if a row was deleted.
func (s *Store) DeleteAlertRule(ctx context.Context, tenantID, id string) (bool, error) {
	const q = `DELETE FROM alert_rules WHERE id = $1 AND tenant_id = $2`

	tag, err := s.Pool.Exec(ctx, q, id, tenantID)
	if err != nil {
		return false, fmt.Errorf("DeleteAlertRule: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ListNotificationChannels returns all notification channels for the given tenant.
func (s *Store) ListNotificationChannels(ctx context.Context, tenantID string) ([]NotificationChannel, error) {
	const q = `
		SELECT id, tenant_id, kind::text, name, config, enabled, created_at
		FROM notification_channels
		WHERE tenant_id = $1
		ORDER BY created_at DESC`

	rows, err := s.Pool.Query(ctx, q, tenantID)
	if err != nil {
		return nil, fmt.Errorf("ListNotificationChannels: %w", err)
	}
	defer rows.Close()

	var channels []NotificationChannel
	for rows.Next() {
		var c NotificationChannel
		if err := rows.Scan(
			&c.ID, &c.TenantID, &c.Kind, &c.Name,
			&c.Config, &c.Enabled, &c.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("ListNotificationChannels scan: %w", err)
		}
		channels = append(channels, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ListNotificationChannels rows: %w", err)
	}
	return channels, nil
}

// CreateNotificationChannel inserts a new notification channel and returns its generated ID.
func (s *Store) CreateNotificationChannel(ctx context.Context, tenantID, kind, name string, config []byte) (string, error) {
	const q = `
		INSERT INTO notification_channels (tenant_id, kind, name, config, enabled, created_at)
		VALUES ($1, $2::text, $3, $4, true, now())
		RETURNING id`

	var id string
	err := s.Pool.QueryRow(ctx, q, tenantID, kind, name, config).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("CreateNotificationChannel: %w", err)
	}
	return id, nil
}

// DeleteNotificationChannel removes a notification channel. Returns true if a row was deleted.
func (s *Store) DeleteNotificationChannel(ctx context.Context, tenantID, id string) (bool, error) {
	const q = `DELETE FROM notification_channels WHERE id = $1 AND tenant_id = $2`

	tag, err := s.Pool.Exec(ctx, q, id, tenantID)
	if err != nil {
		return false, fmt.Errorf("DeleteNotificationChannel: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ListAlertEvents returns the most recent alert events for the given tenant.
func (s *Store) ListAlertEvents(ctx context.Context, tenantID string, limit int) ([]AlertEvent, error) {
	const q = `
		SELECT id, tenant_id, rule_id, state::text, triggered_at, resolved_at, payload, created_at
		FROM alert_events
		WHERE tenant_id = $1
		ORDER BY created_at DESC
		LIMIT $2`

	rows, err := s.Pool.Query(ctx, q, tenantID, limit)
	if err != nil {
		return nil, fmt.Errorf("ListAlertEvents: %w", err)
	}
	defer rows.Close()

	var events []AlertEvent
	for rows.Next() {
		var e AlertEvent
		if err := rows.Scan(
			&e.ID, &e.TenantID, &e.RuleID, &e.State,
			&e.TriggeredAt, &e.ResolvedAt, &e.Payload, &e.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("ListAlertEvents scan: %w", err)
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ListAlertEvents rows: %w", err)
	}
	return events, nil
}

// InsertAlertEvent records a new alert event with state "firing" and returns its generated ID.
func (s *Store) InsertAlertEvent(ctx context.Context, tenantID, ruleID string, payload []byte) (string, error) {
	const q = `
		INSERT INTO alert_events (tenant_id, rule_id, state, triggered_at, payload, created_at)
		VALUES ($1, $2, 'firing'::text, now(), $3, now())
		RETURNING id`

	var id string
	err := s.Pool.QueryRow(ctx, q, tenantID, ruleID, payload).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("InsertAlertEvent: %w", err)
	}
	return id, nil
}

// ensure pgx.ErrNoRows is imported (used transitively via callers).
var _ = pgx.ErrNoRows
