// Package postgres is the PostgreSQL access layer (the operational system-of-record:
// tenants, domains, DNS snapshots, alerts, config). It uses a pgx connection pool. The
// schema it targets is schemas/postgres/001_init.sql; migrations live under /migrations.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zezekim/mxsentinel/internal/config"
)

// Store wraps a pgx connection pool.
type Store struct {
	Pool *pgxpool.Pool
}

// New opens a connection pool using the given config and verifies connectivity.
func New(ctx context.Context, cfg config.PostgresConfig) (*Store, error) {
	pcfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("parse postgres dsn: %w", err)
	}
	if cfg.MaxConns > 0 {
		pcfg.MaxConns = int32(cfg.MaxConns)
	}
	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, fmt.Errorf("create pgx pool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return &Store{Pool: pool}, nil
}

// Ping verifies the database is reachable.
func (s *Store) Ping(ctx context.Context) error { return s.Pool.Ping(ctx) }

// Close releases the pool.
func (s *Store) Close() { s.Pool.Close() }

// Tenant is a minimal projection of the tenants table.
type Tenant struct {
	ID   string
	Name string
	Slug string
	Kind string
}

// Domain is a minimal projection of the domains table.
type Domain struct {
	ID       string
	TenantID string
	Name     string
	Status   string
}

// CreateTenant inserts a tenant and returns its generated id.
func (s *Store) CreateTenant(ctx context.Context, t Tenant) (string, error) {
	const q = `INSERT INTO tenants (name, slug, kind)
	           VALUES ($1, $2, COALESCE(NULLIF($3,'')::tenant_kind, 'enterprise'))
	           RETURNING id`
	var id string
	if err := s.Pool.QueryRow(ctx, q, t.Name, t.Slug, t.Kind).Scan(&id); err != nil {
		return "", fmt.Errorf("create tenant: %w", err)
	}
	return id, nil
}

// GetTenantBySlug looks up a tenant by its unique slug.
func (s *Store) GetTenantBySlug(ctx context.Context, slug string) (Tenant, error) {
	const q = `SELECT id, name, slug, kind::text FROM tenants WHERE slug = $1`
	var t Tenant
	if err := s.Pool.QueryRow(ctx, q, slug).Scan(&t.ID, &t.Name, &t.Slug, &t.Kind); err != nil {
		return Tenant{}, fmt.Errorf("get tenant by slug %q: %w", slug, err)
	}
	return t, nil
}

// CreateDomain inserts a monitored domain for a tenant and returns its id.
func (s *Store) CreateDomain(ctx context.Context, tenantID, name string) (string, error) {
	const q = `INSERT INTO domains (tenant_id, name) VALUES ($1, $2) RETURNING id`
	var id string
	if err := s.Pool.QueryRow(ctx, q, tenantID, name).Scan(&id); err != nil {
		return "", fmt.Errorf("create domain: %w", err)
	}
	return id, nil
}

// DeleteDomain removes a domain (and all its snapshots/findings via CASCADE) for the given
// tenant. Returns found=false (nil error) when no matching row exists.
func (s *Store) DeleteDomain(ctx context.Context, tenantID, domainID string) (bool, error) {
	const q = `DELETE FROM domains WHERE id = $1 AND tenant_id = $2`
	tag, err := s.Pool.Exec(ctx, q, domainID, tenantID)
	if err != nil {
		return false, fmt.Errorf("delete domain: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// UpdateDomainStatus sets the status of a tenant-owned domain. Valid values are the
// domain_status enum members: monitored, paused, pending_verification, error.
// Returns found=false (nil error) when no matching row exists.
func (s *Store) UpdateDomainStatus(ctx context.Context, tenantID, domainID, status string) (bool, error) {
	const q = `UPDATE domains SET status = $3::domain_status WHERE id = $1 AND tenant_id = $2`
	tag, err := s.Pool.Exec(ctx, q, domainID, tenantID, status)
	if err != nil {
		return false, fmt.Errorf("update domain status: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// EnsureDomain registers a sending domain for a tenant if it isn't already, returning
// created=true only when a new row was inserted. Idempotent — safe for bulk imports.
func (s *Store) EnsureDomain(ctx context.Context, tenantID, name string) (created bool, err error) {
	const q = `INSERT INTO domains (tenant_id, name) VALUES ($1, $2)
	           ON CONFLICT (tenant_id, name) DO NOTHING`
	tag, err := s.Pool.Exec(ctx, q, tenantID, name)
	if err != nil {
		return false, fmt.Errorf("ensure domain %q: %w", name, err)
	}
	return tag.RowsAffected() > 0, nil
}

// ResolveTenantByDomain returns the tenant that owns a sending domain. The bool is false
// (with nil error) when no such domain is registered.
func (s *Store) ResolveTenantByDomain(ctx context.Context, name string) (string, bool, error) {
	const q = `SELECT tenant_id FROM domains WHERE name = $1 LIMIT 1`
	var tenantID string
	err := s.Pool.QueryRow(ctx, q, name).Scan(&tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("resolve tenant by domain %q: %w", name, err)
	}
	return tenantID, true, nil
}

// ListDomains returns all domains for a tenant.
func (s *Store) ListDomains(ctx context.Context, tenantID string) ([]Domain, error) {
	const q = `SELECT id, tenant_id, name, status::text FROM domains WHERE tenant_id = $1 ORDER BY name`
	rows, err := s.Pool.Query(ctx, q, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list domains: %w", err)
	}
	defer rows.Close()

	var out []Domain
	for rows.Next() {
		var d Domain
		if err := rows.Scan(&d.ID, &d.TenantID, &d.Name, &d.Status); err != nil {
			return nil, fmt.Errorf("scan domain: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
