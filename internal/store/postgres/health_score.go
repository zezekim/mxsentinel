package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// HealthScoreSnapshot is one persisted Deliverability Health Score row for a domain. Components
// is the raw JSONB breakdown (a marshalled []healthscore.ComponentScore) preserved verbatim so
// the API can return it and the AI layer can cite it without re-deriving anything.
type HealthScoreSnapshot struct {
	ID         string
	TenantID   string
	DomainID   string
	DomainName string
	Score      float64
	Grade      string
	HasData    bool
	Coverage   float64
	Components json.RawMessage
	ComputedAt time.Time
}

// InsertHealthScoreSnapshot appends a snapshot and returns its generated id. components must be
// valid JSON (an array); pass nil for an empty breakdown.
func (s *Store) InsertHealthScoreSnapshot(ctx context.Context, snap HealthScoreSnapshot) (string, error) {
	comps := snap.Components
	if len(comps) == 0 {
		comps = json.RawMessage("[]")
	}
	const q = `
		INSERT INTO health_score_snapshots
			(tenant_id, domain_id, domain_name, score, grade, has_data, coverage, components, computed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, COALESCE($9, now()))
		RETURNING id`
	var computedAt *time.Time
	if !snap.ComputedAt.IsZero() {
		computedAt = &snap.ComputedAt
	}
	var id string
	if err := s.Pool.QueryRow(ctx, q,
		snap.TenantID, snap.DomainID, snap.DomainName, snap.Score, snap.Grade,
		snap.HasData, snap.Coverage, comps, computedAt,
	).Scan(&id); err != nil {
		return "", fmt.Errorf("insert health score snapshot: %w", err)
	}
	return id, nil
}

// LatestHealthScores returns the most recent snapshot for every domain of a tenant (one row per
// domain), ordered worst score first so the dashboard surfaces the problems on top. Domains that
// have never been scored are omitted.
func (s *Store) LatestHealthScores(ctx context.Context, tenantID string) ([]HealthScoreSnapshot, error) {
	const q = `
		SELECT DISTINCT ON (domain_id)
		       id, tenant_id, domain_id, domain_name, score, grade, has_data, coverage, components, computed_at
		FROM health_score_snapshots
		WHERE tenant_id = $1
		ORDER BY domain_id, computed_at DESC`
	rows, err := s.Pool.Query(ctx, q, tenantID)
	if err != nil {
		return nil, fmt.Errorf("latest health scores: %w", err)
	}
	defer rows.Close()

	out, err := scanHealthScoreRows(rows)
	if err != nil {
		return nil, err
	}
	// Sort worst-first (lowest score) in Go; DISTINCT ON forces a domain_id ordering in SQL.
	sortSnapshotsWorstFirst(out)
	return out, nil
}

// LatestHealthScoreForDomain returns the newest snapshot for one domain (scoped to tenant).
// found=false (nil error) when the domain has no snapshot yet.
func (s *Store) LatestHealthScoreForDomain(ctx context.Context, tenantID, domainID string) (HealthScoreSnapshot, bool, error) {
	const q = `
		SELECT id, tenant_id, domain_id, domain_name, score, grade, has_data, coverage, components, computed_at
		FROM health_score_snapshots
		WHERE tenant_id = $1 AND domain_id = $2
		ORDER BY computed_at DESC
		LIMIT 1`
	snap, err := scanHealthScoreRow(s.Pool.QueryRow(ctx, q, tenantID, domainID))
	if errors.Is(err, pgx.ErrNoRows) {
		return HealthScoreSnapshot{}, false, nil
	}
	if err != nil {
		return HealthScoreSnapshot{}, false, fmt.Errorf("latest health score for domain: %w", err)
	}
	return snap, true, nil
}

// HealthScoreHistory returns a domain's snapshots newest-first (the trend timeline), scoped to
// tenant. limit defaults to 100 (<=0), capped at 1000.
func (s *Store) HealthScoreHistory(ctx context.Context, tenantID, domainID string, limit int) ([]HealthScoreSnapshot, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	const q = `
		SELECT id, tenant_id, domain_id, domain_name, score, grade, has_data, coverage, components, computed_at
		FROM health_score_snapshots
		WHERE tenant_id = $1 AND domain_id = $2
		ORDER BY computed_at DESC
		LIMIT $3`
	rows, err := s.Pool.Query(ctx, q, tenantID, domainID, limit)
	if err != nil {
		return nil, fmt.Errorf("health score history: %w", err)
	}
	defer rows.Close()
	return scanHealthScoreRows(rows)
}

// TenantDomainRef is a minimal (tenant, domain) pair used by cmd/scored to know what to score.
type TenantDomainRef struct {
	TenantID   string
	DomainID   string
	DomainName string
}

// AllMonitoredDomains returns every domain across all tenants that is not paused — the work list
// for the health-score snapshotter. It deliberately spans tenants (cmd/scored is a platform
// daemon, like repd), so it is not tenant-scoped.
func (s *Store) AllMonitoredDomains(ctx context.Context) ([]TenantDomainRef, error) {
	const q = `
		SELECT tenant_id::text, id::text, name
		FROM domains
		WHERE status::text <> 'paused'
		ORDER BY tenant_id, name`
	rows, err := s.Pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("all monitored domains: %w", err)
	}
	defer rows.Close()

	var out []TenantDomainRef
	for rows.Next() {
		var r TenantDomainRef
		if err := rows.Scan(&r.TenantID, &r.DomainID, &r.DomainName); err != nil {
			return nil, fmt.Errorf("scan monitored domain: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func scanHealthScoreRow(row pgx.Row) (HealthScoreSnapshot, error) {
	var snap HealthScoreSnapshot
	if err := row.Scan(
		&snap.ID, &snap.TenantID, &snap.DomainID, &snap.DomainName,
		&snap.Score, &snap.Grade, &snap.HasData, &snap.Coverage,
		&snap.Components, &snap.ComputedAt,
	); err != nil {
		return HealthScoreSnapshot{}, err
	}
	return snap, nil
}

func scanHealthScoreRows(rows pgx.Rows) ([]HealthScoreSnapshot, error) {
	var out []HealthScoreSnapshot
	for rows.Next() {
		snap, err := scanHealthScoreRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scan health score snapshot: %w", err)
		}
		out = append(out, snap)
	}
	return out, rows.Err()
}

func sortSnapshotsWorstFirst(s []HealthScoreSnapshot) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j].Score < s[j-1].Score; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
