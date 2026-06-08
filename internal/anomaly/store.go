// Package anomaly implements send-volume anomaly detection for the outbound relay. It
// tracks a per-(tenant, sender_domain) rolling hourly message count, maintains an EWMA
// hourly baseline persisted in Postgres (sender_volume_baseline), and trips a spike when
// the current-hour count exceeds max(baseline*FACTOR, MIN_ABSOLUTE) once a domain is
// warmed up (samples >= MIN_SAMPLES). Detections are logged to volume_anomaly and raise a
// critical incident.
//
// On a SHARED relay one SASL credential relays for hundreds of client domains, so the
// signal is keyed on the From: domain, NOT the SASL login (which is shared and would
// alias every client together). This complements rspamd's inline rate caps: rspamd holds
// an absolute ceiling, anomalyd catches relative spikes below that ceiling.
package anomaly

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Baseline is the learned per-domain hourly EWMA and its sample count.
type Baseline struct {
	EWMA    float64
	Samples int64
}

// Store is the Postgres access layer for anomaly state. It runs SQL through the shared
// pgxpool.Pool handle so it needs no files under internal/store/postgres.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore wraps a pgx pool (obtained from (*postgres.Store).Pool).
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// GetBaseline returns the persisted EWMA baseline for a domain. found=false (nil error)
// when the domain has never been recorded.
func (s *Store) GetBaseline(ctx context.Context, tenantID, domain string) (Baseline, bool, error) {
	const q = `SELECT ewma_hourly, samples FROM sender_volume_baseline
	           WHERE tenant_id = $1 AND sender_domain = $2`
	var b Baseline
	err := s.pool.QueryRow(ctx, q, tenantID, domain).Scan(&b.EWMA, &b.Samples)
	if err == pgx.ErrNoRows {
		return Baseline{}, false, nil
	}
	if err != nil {
		return Baseline{}, false, fmt.Errorf("get baseline %s/%s: %w", tenantID, domain, err)
	}
	return b, true, nil
}

// UpsertBaseline writes the new EWMA + sample count for a domain.
func (s *Store) UpsertBaseline(ctx context.Context, tenantID, domain string, ewma float64, samples int64) error {
	const q = `INSERT INTO sender_volume_baseline (tenant_id, sender_domain, ewma_hourly, samples, updated_at)
	           VALUES ($1, $2, $3, $4, now())
	           ON CONFLICT (tenant_id, sender_domain)
	           DO UPDATE SET ewma_hourly = EXCLUDED.ewma_hourly,
	                         samples     = EXCLUDED.samples,
	                         updated_at  = now()`
	if _, err := s.pool.Exec(ctx, q, tenantID, domain, ewma, samples); err != nil {
		return fmt.Errorf("upsert baseline %s/%s: %w", tenantID, domain, err)
	}
	return nil
}

// InsertAnomaly appends a tripped spike to volume_anomaly.
func (s *Store) InsertAnomaly(ctx context.Context, tenantID, domain string, observed int64, baseline, factor float64) error {
	const q = `INSERT INTO volume_anomaly (tenant_id, sender_domain, observed_hour_count, baseline, factor)
	           VALUES ($1, $2, $3, $4, $5)`
	if _, err := s.pool.Exec(ctx, q, tenantID, domain, observed, baseline, factor); err != nil {
		return fmt.Errorf("insert anomaly %s/%s: %w", tenantID, domain, err)
	}
	return nil
}

// AnomalyRow is a recent detection for the API.
type AnomalyRow struct {
	SenderDomain      string
	ObservedHourCount int64
	Baseline          float64
	Factor            float64
	DetectedAt        time.Time
}

// RecentAnomalies returns a tenant's most recent detections, newest first.
func (s *Store) RecentAnomalies(ctx context.Context, tenantID string, limit int) ([]AnomalyRow, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	const q = `SELECT sender_domain, observed_hour_count, baseline, factor, detected_at
	           FROM volume_anomaly WHERE tenant_id = $1
	           ORDER BY detected_at DESC LIMIT $2`
	rows, err := s.pool.Query(ctx, q, tenantID, limit)
	if err != nil {
		return nil, fmt.Errorf("recent anomalies: %w", err)
	}
	defer rows.Close()

	var out []AnomalyRow
	for rows.Next() {
		var a AnomalyRow
		if err := rows.Scan(&a.SenderDomain, &a.ObservedHourCount, &a.Baseline, &a.Factor, &a.DetectedAt); err != nil {
			return nil, fmt.Errorf("scan anomaly: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// MoverRow ranks a domain by how far its latest detection's observed volume sits above
// its baseline — the "top movers" the dashboard highlights.
type MoverRow struct {
	SenderDomain string
	Current      int64
	Baseline     float64
	Ratio        float64
}

// TopMovers returns the domains with the largest recent observed/baseline ratio (one row
// per domain — its most recent detection within the lookback window).
func (s *Store) TopMovers(ctx context.Context, tenantID string, lookback time.Duration, limit int) ([]MoverRow, error) {
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}
	since := time.Now().Add(-lookback)
	// DISTINCT ON keeps the newest detection per domain; the outer query ranks by factor.
	const q = `
SELECT sender_domain, observed_hour_count, baseline, factor FROM (
    SELECT DISTINCT ON (sender_domain)
           sender_domain, observed_hour_count, baseline, factor, detected_at
    FROM volume_anomaly
    WHERE tenant_id = $1 AND detected_at >= $2
    ORDER BY sender_domain, detected_at DESC
) latest
ORDER BY factor DESC
LIMIT $3`
	rows, err := s.pool.Query(ctx, q, tenantID, since, limit)
	if err != nil {
		return nil, fmt.Errorf("top movers: %w", err)
	}
	defer rows.Close()

	var out []MoverRow
	for rows.Next() {
		var m MoverRow
		if err := rows.Scan(&m.SenderDomain, &m.Current, &m.Baseline, &m.Ratio); err != nil {
			return nil, fmt.Errorf("scan mover: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
