package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// AlertChannel is a row in the alert_channels table. Config holds the raw JSONB config with
// sensitive fields encrypted at rest (see internal/alertchannels.SealConfig).
type AlertChannel struct {
	ID        string
	TenantID  string
	Type      string
	Name      string
	Config    []byte // JSONB raw; secrets encrypted
	Enabled   bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// AlertDelivery is a row in the alert_deliveries log.
type AlertDelivery struct {
	ID        string
	ChannelID string
	AlertRef  string
	Status    string
	Error     string
	SentAt    time.Time
}

// FiringIncident is the minimal projection notifyd needs to fan an open incident out to a
// tenant's channels. It is read from the incidents table.
type FiringIncident struct {
	ID        string
	TenantID  string
	Kind      string
	Severity  string
	Domain    string
	Title     string
	CreatedAt time.Time
}

const alertChannelColumns = `id, tenant_id, type, name, config_json, enabled, created_at, updated_at`

// ListAlertChannels returns all alert channels for a tenant, newest first.
func (s *Store) ListAlertChannels(ctx context.Context, tenantID string) ([]AlertChannel, error) {
	const q = `SELECT ` + alertChannelColumns + `
		FROM alert_channels WHERE tenant_id = $1 ORDER BY created_at DESC`
	return s.queryAlertChannels(ctx, q, tenantID)
}

// ListEnabledAlertChannels returns the enabled alert channels for a tenant.
func (s *Store) ListEnabledAlertChannels(ctx context.Context, tenantID string) ([]AlertChannel, error) {
	const q = `SELECT ` + alertChannelColumns + `
		FROM alert_channels WHERE tenant_id = $1 AND enabled ORDER BY created_at DESC`
	return s.queryAlertChannels(ctx, q, tenantID)
}

func (s *Store) queryAlertChannels(ctx context.Context, q string, args ...any) ([]AlertChannel, error) {
	rows, err := s.Pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query alert channels: %w", err)
	}
	defer rows.Close()

	var out []AlertChannel
	for rows.Next() {
		var c AlertChannel
		if err := rows.Scan(&c.ID, &c.TenantID, &c.Type, &c.Name, &c.Config, &c.Enabled, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan alert channel: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetAlertChannel fetches one channel by id, scoped to tenant. found=false if absent.
func (s *Store) GetAlertChannel(ctx context.Context, tenantID, id string) (AlertChannel, bool, error) {
	const q = `SELECT ` + alertChannelColumns + `
		FROM alert_channels WHERE id = $1 AND tenant_id = $2`
	var c AlertChannel
	err := s.Pool.QueryRow(ctx, q, id, tenantID).Scan(
		&c.ID, &c.TenantID, &c.Type, &c.Name, &c.Config, &c.Enabled, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AlertChannel{}, false, nil
		}
		return AlertChannel{}, false, fmt.Errorf("get alert channel: %w", err)
	}
	return c, true, nil
}

// CreateAlertChannel inserts a channel (config already sealed) and returns its id.
func (s *Store) CreateAlertChannel(ctx context.Context, tenantID, chType, name string, config []byte) (string, error) {
	const q = `
		INSERT INTO alert_channels (tenant_id, type, name, config_json, enabled, created_at, updated_at)
		VALUES ($1, $2, $3, $4, true, now(), now())
		RETURNING id`
	var id string
	if err := s.Pool.QueryRow(ctx, q, tenantID, chType, name, config).Scan(&id); err != nil {
		return "", fmt.Errorf("create alert channel: %w", err)
	}
	return id, nil
}

// UpdateAlertChannel patches name, enabled, and config (config already sealed). A nil
// config leaves the stored config untouched. Returns found=false if no such row.
func (s *Store) UpdateAlertChannel(ctx context.Context, tenantID, id, name string, enabled bool, config []byte) (bool, error) {
	const q = `
		UPDATE alert_channels
		SET name = $1, enabled = $2,
		    config_json = COALESCE($3, config_json),
		    updated_at = now()
		WHERE id = $4 AND tenant_id = $5`
	var cfgArg any
	if config != nil {
		cfgArg = config
	}
	tag, err := s.Pool.Exec(ctx, q, name, enabled, cfgArg, id, tenantID)
	if err != nil {
		return false, fmt.Errorf("update alert channel: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// DeleteAlertChannel removes a channel. Returns found=false if no such row.
func (s *Store) DeleteAlertChannel(ctx context.Context, tenantID, id string) (bool, error) {
	const q = `DELETE FROM alert_channels WHERE id = $1 AND tenant_id = $2`
	tag, err := s.Pool.Exec(ctx, q, id, tenantID)
	if err != nil {
		return false, fmt.Errorf("delete alert channel: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ---- alert_deliveries (dedup / throttle / audit) --------------------------------------

// DeliveredFor reports whether a successful delivery for (channelID, alertRef) exists within
// the window. Implements alertchannels.DeliveryStore. Drives dedup.
func (s *Store) DeliveredFor(ctx context.Context, channelID, alertRef string, within time.Duration) (bool, error) {
	const q = `
		SELECT EXISTS (
			SELECT 1 FROM alert_deliveries
			WHERE channel_id = $1 AND alert_ref = $2 AND status = 'sent'
			  AND sent_at >= now() - $3::interval
		)`
	var exists bool
	if err := s.Pool.QueryRow(ctx, q, channelID, alertRef, intervalString(within)).Scan(&exists); err != nil {
		return false, fmt.Errorf("delivered-for check: %w", err)
	}
	return exists, nil
}

// LastSentToChannel reports whether any successful delivery to channelID happened within the
// window. Implements alertchannels.DeliveryStore. Drives per-channel throttling.
func (s *Store) LastSentToChannel(ctx context.Context, channelID string, within time.Duration) (bool, error) {
	const q = `
		SELECT EXISTS (
			SELECT 1 FROM alert_deliveries
			WHERE channel_id = $1 AND status = 'sent'
			  AND sent_at >= now() - $2::interval
		)`
	var exists bool
	if err := s.Pool.QueryRow(ctx, q, channelID, intervalString(within)).Scan(&exists); err != nil {
		return false, fmt.Errorf("last-sent check: %w", err)
	}
	return exists, nil
}

// Record appends a delivery-log row, deriving tenant_id from the channel. Implements
// alertchannels.DeliveryStore.
func (s *Store) Record(ctx context.Context, channelID, alertRef, status, errMsg string) error {
	const q = `
		INSERT INTO alert_deliveries (tenant_id, channel_id, alert_ref, status, error, sent_at)
		SELECT tenant_id, id, $2, $3, $4, now() FROM alert_channels WHERE id = $1`
	if _, err := s.Pool.Exec(ctx, q, channelID, alertRef, status, errMsg); err != nil {
		return fmt.Errorf("record delivery: %w", err)
	}
	return nil
}

// ListAlertDeliveries returns the most recent delivery-log rows for a tenant.
func (s *Store) ListAlertDeliveries(ctx context.Context, tenantID string, limit int) ([]AlertDelivery, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	const q = `
		SELECT id, channel_id, alert_ref, status, error, sent_at
		FROM alert_deliveries WHERE tenant_id = $1
		ORDER BY sent_at DESC LIMIT $2`
	rows, err := s.Pool.Query(ctx, q, tenantID, limit)
	if err != nil {
		return nil, fmt.Errorf("list alert deliveries: %w", err)
	}
	defer rows.Close()

	var out []AlertDelivery
	for rows.Next() {
		var d AlertDelivery
		if err := rows.Scan(&d.ID, &d.ChannelID, &d.AlertRef, &d.Status, &d.Error, &d.SentAt); err != nil {
			return nil, fmt.Errorf("scan alert delivery: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ListFiringIncidentsSince returns open incidents across all tenants created at or after
// `since`, oldest first. notifyd uses this as its firing-alert feed; dedup on the
// delivery log prevents re-notifying on overlapping scans.
func (s *Store) ListFiringIncidentsSince(ctx context.Context, since time.Time, limit int) ([]FiringIncident, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	const q = `
		SELECT id, tenant_id, kind::text, severity::text, COALESCE(domain, ''), title, created_at
		FROM incidents
		WHERE status = 'open' AND created_at >= $1
		ORDER BY created_at ASC
		LIMIT $2`
	rows, err := s.Pool.Query(ctx, q, since, limit)
	if err != nil {
		return nil, fmt.Errorf("list firing incidents: %w", err)
	}
	defer rows.Close()

	var out []FiringIncident
	for rows.Next() {
		var f FiringIncident
		if err := rows.Scan(&f.ID, &f.TenantID, &f.Kind, &f.Severity, &f.Domain, &f.Title, &f.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan firing incident: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// intervalString renders a duration as a Postgres interval literal in seconds.
func intervalString(d time.Duration) string {
	secs := int64(d / time.Second)
	if secs < 0 {
		secs = 0
	}
	return fmt.Sprintf("%d seconds", secs)
}
