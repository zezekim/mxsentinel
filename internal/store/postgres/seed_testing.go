package postgres

import (
	"context"
	"fmt"
	"time"
)

// ── Row types ───────────────────────────────────────────────────────────────

// SeedList is a per-tenant named collection of seed mailboxes.
type SeedList struct {
	ID           string
	TenantID     string
	Name         string
	Description  string
	AddressCount int
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// SeedAddress is one seed mailbox within a list.
type SeedAddress struct {
	ID        string
	TenantID  string
	ListID    string
	Address   string
	Provider  string
	Enabled   bool
	CreatedAt time.Time
}

// SeedRun is one execution of a seed list.
type SeedRun struct {
	ID          string
	TenantID    string
	ListID      *string
	Name        string
	RunTag      string
	FromAddress string
	IPPool      string
	Status      string
	SeedCount   int
	SentCount   int
	StartedAt   *time.Time
	CompletedAt *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// SeedResult is one seed's outcome within a run.
type SeedResult struct {
	ID         string
	RunID      string
	TenantID   string
	Address    string
	Provider   string
	ProbeTag   string
	Status     string
	Placement  string
	Mailbox    string
	SPFPass    *bool
	DKIMPass   *bool
	DMARCPass  *bool
	Detail     string
	SentAt     *time.Time
	ObservedAt *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// NewSeedResultInput seeds one seed_results row when a run is created.
type NewSeedResultInput struct {
	Address  string
	Provider string
	ProbeTag string
}

// ── Seed lists ──────────────────────────────────────────────────────────────

// ListSeedLists returns all seed lists for a tenant with their address counts.
func (s *Store) ListSeedLists(ctx context.Context, tenantID string) ([]SeedList, error) {
	const q = `
		SELECT l.id, l.tenant_id, l.name, l.description,
		       (SELECT count(*) FROM seed_addresses a WHERE a.list_id = l.id),
		       l.created_at, l.updated_at
		FROM seed_lists l
		WHERE l.tenant_id = $1
		ORDER BY l.created_at DESC`
	rows, err := s.Pool.Query(ctx, q, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list seed lists: %w", err)
	}
	defer rows.Close()

	var out []SeedList
	for rows.Next() {
		var l SeedList
		if err := rows.Scan(&l.ID, &l.TenantID, &l.Name, &l.Description, &l.AddressCount, &l.CreatedAt, &l.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan seed list: %w", err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// GetSeedList fetches one list by tenant + id.
func (s *Store) GetSeedList(ctx context.Context, tenantID, id string) (SeedList, error) {
	const q = `
		SELECT l.id, l.tenant_id, l.name, l.description,
		       (SELECT count(*) FROM seed_addresses a WHERE a.list_id = l.id),
		       l.created_at, l.updated_at
		FROM seed_lists l
		WHERE l.tenant_id = $1 AND l.id = $2`
	var l SeedList
	if err := s.Pool.QueryRow(ctx, q, tenantID, id).Scan(
		&l.ID, &l.TenantID, &l.Name, &l.Description, &l.AddressCount, &l.CreatedAt, &l.UpdatedAt,
	); err != nil {
		return SeedList{}, fmt.Errorf("get seed list: %w", err)
	}
	return l, nil
}

// CreateSeedList inserts a new seed list and returns its id.
func (s *Store) CreateSeedList(ctx context.Context, tenantID, name, description string) (string, error) {
	const q = `
		INSERT INTO seed_lists (tenant_id, name, description, created_at, updated_at)
		VALUES ($1, $2, $3, now(), now())
		RETURNING id`
	var id string
	if err := s.Pool.QueryRow(ctx, q, tenantID, name, description).Scan(&id); err != nil {
		return "", fmt.Errorf("create seed list: %w", err)
	}
	return id, nil
}

// DeleteSeedList removes a list (cascading to its addresses). Returns true if a row was deleted.
func (s *Store) DeleteSeedList(ctx context.Context, tenantID, id string) (bool, error) {
	const q = `DELETE FROM seed_lists WHERE tenant_id = $1 AND id = $2`
	tag, err := s.Pool.Exec(ctx, q, tenantID, id)
	if err != nil {
		return false, fmt.Errorf("delete seed list: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ── Seed addresses ──────────────────────────────────────────────────────────

// AddSeedAddress inserts a seed mailbox into a list. The list must belong to the tenant.
func (s *Store) AddSeedAddress(ctx context.Context, tenantID, listID, address, provider string) (string, error) {
	if provider == "" {
		provider = "other"
	}
	const q = `
		INSERT INTO seed_addresses (tenant_id, list_id, address, provider)
		SELECT $1, $2, $3, $4
		WHERE EXISTS (SELECT 1 FROM seed_lists WHERE id = $2 AND tenant_id = $1)
		ON CONFLICT (list_id, address) DO UPDATE SET provider = EXCLUDED.provider, enabled = true
		RETURNING id`
	var id string
	if err := s.Pool.QueryRow(ctx, q, tenantID, listID, address, provider).Scan(&id); err != nil {
		return "", fmt.Errorf("add seed address: %w", err)
	}
	return id, nil
}

// ListSeedAddresses returns all addresses in a list.
func (s *Store) ListSeedAddresses(ctx context.Context, listID string) ([]SeedAddress, error) {
	const q = `
		SELECT id, tenant_id, list_id, address, provider, enabled, created_at
		FROM seed_addresses
		WHERE list_id = $1
		ORDER BY provider, address`
	rows, err := s.Pool.Query(ctx, q, listID)
	if err != nil {
		return nil, fmt.Errorf("list seed addresses: %w", err)
	}
	defer rows.Close()

	var out []SeedAddress
	for rows.Next() {
		var a SeedAddress
		if err := rows.Scan(&a.ID, &a.TenantID, &a.ListID, &a.Address, &a.Provider, &a.Enabled, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan seed address: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// DeleteSeedAddress removes an address from a list (tenant-scoped). Returns true if deleted.
func (s *Store) DeleteSeedAddress(ctx context.Context, tenantID, id string) (bool, error) {
	const q = `DELETE FROM seed_addresses WHERE tenant_id = $1 AND id = $2`
	tag, err := s.Pool.Exec(ctx, q, tenantID, id)
	if err != nil {
		return false, fmt.Errorf("delete seed address: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ── Seed runs & results ─────────────────────────────────────────────────────

// CreateSeedRun inserts a run plus one seed_results row per input, in a single transaction.
// listID may be empty (the run is not tied to a stored list). Returns the new run id.
func (s *Store) CreateSeedRun(ctx context.Context, tenantID, listID, name, runTag, fromAddress, ipPool string, results []NewSeedResultInput) (string, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin seed run tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var listArg any
	if listID != "" {
		listArg = listID
	}
	const insRun = `
		INSERT INTO seed_runs (tenant_id, list_id, name, run_tag, from_address, ip_pool, status, seed_count, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'pending', $7, now(), now())
		RETURNING id`
	var runID string
	if err := tx.QueryRow(ctx, insRun, tenantID, listArg, name, runTag, fromAddress, ipPool, len(results)).Scan(&runID); err != nil {
		return "", fmt.Errorf("insert seed run: %w", err)
	}

	const insResult = `
		INSERT INTO seed_results (run_id, tenant_id, address, provider, probe_tag, status, placement, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, 'pending', 'unknown', now(), now())`
	for _, r := range results {
		provider := r.Provider
		if provider == "" {
			provider = "other"
		}
		if _, err := tx.Exec(ctx, insResult, runID, tenantID, r.Address, provider, r.ProbeTag); err != nil {
			return "", fmt.Errorf("insert seed result: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit seed run: %w", err)
	}
	return runID, nil
}

// ListSeedRuns returns runs for a tenant, newest first, capped by limit (<=0 => 50).
func (s *Store) ListSeedRuns(ctx context.Context, tenantID string, limit int) ([]SeedRun, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	const q = `
		SELECT id, tenant_id, list_id, name, run_tag, from_address, ip_pool, status,
		       seed_count, sent_count, started_at, completed_at, created_at, updated_at
		FROM seed_runs
		WHERE tenant_id = $1
		ORDER BY created_at DESC
		LIMIT $2`
	rows, err := s.Pool.Query(ctx, q, tenantID, limit)
	if err != nil {
		return nil, fmt.Errorf("list seed runs: %w", err)
	}
	defer rows.Close()
	return scanSeedRuns(rows)
}

// GetSeedRun fetches a single run by tenant + id.
func (s *Store) GetSeedRun(ctx context.Context, tenantID, id string) (SeedRun, error) {
	const q = `
		SELECT id, tenant_id, list_id, name, run_tag, from_address, ip_pool, status,
		       seed_count, sent_count, started_at, completed_at, created_at, updated_at
		FROM seed_runs
		WHERE tenant_id = $1 AND id = $2`
	var r SeedRun
	if err := s.Pool.QueryRow(ctx, q, tenantID, id).Scan(
		&r.ID, &r.TenantID, &r.ListID, &r.Name, &r.RunTag, &r.FromAddress, &r.IPPool, &r.Status,
		&r.SeedCount, &r.SentCount, &r.StartedAt, &r.CompletedAt, &r.CreatedAt, &r.UpdatedAt,
	); err != nil {
		return SeedRun{}, fmt.Errorf("get seed run: %w", err)
	}
	return r, nil
}

// ListActiveSeedRuns returns every run not yet in a terminal state, across all tenants — the
// work queue seedd advances on each tick.
func (s *Store) ListActiveSeedRuns(ctx context.Context) ([]SeedRun, error) {
	const q = `
		SELECT id, tenant_id, list_id, name, run_tag, from_address, ip_pool, status,
		       seed_count, sent_count, started_at, completed_at, created_at, updated_at
		FROM seed_runs
		WHERE status NOT IN ('completed', 'failed')
		ORDER BY created_at ASC`
	rows, err := s.Pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list active seed runs: %w", err)
	}
	defer rows.Close()
	return scanSeedRuns(rows)
}

func scanSeedRuns(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
}) ([]SeedRun, error) {
	var out []SeedRun
	for rows.Next() {
		var r SeedRun
		if err := rows.Scan(
			&r.ID, &r.TenantID, &r.ListID, &r.Name, &r.RunTag, &r.FromAddress, &r.IPPool, &r.Status,
			&r.SeedCount, &r.SentCount, &r.StartedAt, &r.CompletedAt, &r.CreatedAt, &r.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan seed run: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListSeedResults returns all results for a run, ordered by provider then address.
func (s *Store) ListSeedResults(ctx context.Context, runID string) ([]SeedResult, error) {
	const q = `
		SELECT id, run_id, tenant_id, address, provider, probe_tag, status, placement, mailbox,
		       spf_pass, dkim_pass, dmarc_pass, detail, sent_at, observed_at, created_at, updated_at
		FROM seed_results
		WHERE run_id = $1
		ORDER BY provider, address`
	rows, err := s.Pool.Query(ctx, q, runID)
	if err != nil {
		return nil, fmt.Errorf("list seed results: %w", err)
	}
	defer rows.Close()
	return scanSeedResults(rows)
}

// ListSeedResultsByStatus returns a run's results in a given status (used by seedd).
func (s *Store) ListSeedResultsByStatus(ctx context.Context, runID, status string) ([]SeedResult, error) {
	const q = `
		SELECT id, run_id, tenant_id, address, provider, probe_tag, status, placement, mailbox,
		       spf_pass, dkim_pass, dmarc_pass, detail, sent_at, observed_at, created_at, updated_at
		FROM seed_results
		WHERE run_id = $1 AND status = $2
		ORDER BY created_at`
	rows, err := s.Pool.Query(ctx, q, runID, status)
	if err != nil {
		return nil, fmt.Errorf("list seed results by status: %w", err)
	}
	defer rows.Close()
	return scanSeedResults(rows)
}

func scanSeedResults(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
}) ([]SeedResult, error) {
	var out []SeedResult
	for rows.Next() {
		var r SeedResult
		if err := rows.Scan(
			&r.ID, &r.RunID, &r.TenantID, &r.Address, &r.Provider, &r.ProbeTag, &r.Status, &r.Placement, &r.Mailbox,
			&r.SPFPass, &r.DKIMPass, &r.DMARCPass, &r.Detail, &r.SentAt, &r.ObservedAt, &r.CreatedAt, &r.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan seed result: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// MarkSeedResultSent flips a pending result to "sent" and stamps sent_at.
func (s *Store) MarkSeedResultSent(ctx context.Context, id string, at time.Time) error {
	const q = `UPDATE seed_results SET status = 'sent', sent_at = $2, updated_at = now() WHERE id = $1`
	if _, err := s.Pool.Exec(ctx, q, id, at); err != nil {
		return fmt.Errorf("mark seed result sent: %w", err)
	}
	return nil
}

// MarkSeedResultError records a send/collect failure on a result without finalizing placement.
func (s *Store) MarkSeedResultError(ctx context.Context, id, detail string) error {
	const q = `UPDATE seed_results SET status = 'error', detail = $2, updated_at = now() WHERE id = $1`
	if _, err := s.Pool.Exec(ctx, q, id, detail); err != nil {
		return fmt.Errorf("mark seed result error: %w", err)
	}
	return nil
}

// FinalizeSeedResult writes a terminal placement (inbox/spam/missing) plus parsed auth flags.
func (s *Store) FinalizeSeedResult(ctx context.Context, id, status, placement, mailbox string, spf, dkim, dmarc *bool, at time.Time) error {
	const q = `
		UPDATE seed_results
		SET status = $2, placement = $3, mailbox = $4,
		    spf_pass = $5, dkim_pass = $6, dmarc_pass = $7,
		    observed_at = $8, updated_at = now()
		WHERE id = $1`
	if _, err := s.Pool.Exec(ctx, q, id, status, placement, mailbox, spf, dkim, dmarc, at); err != nil {
		return fmt.Errorf("finalize seed result: %w", err)
	}
	return nil
}

// SetSeedRunStatus updates a run's status column.
func (s *Store) SetSeedRunStatus(ctx context.Context, id, status string) error {
	const q = `UPDATE seed_runs SET status = $2, updated_at = now() WHERE id = $1`
	if _, err := s.Pool.Exec(ctx, q, id, status); err != nil {
		return fmt.Errorf("set seed run status: %w", err)
	}
	return nil
}

// MarkSeedRunCollecting sets status=collecting, records started_at (first send), and the count
// of probes actually sent.
func (s *Store) MarkSeedRunCollecting(ctx context.Context, id string, sentCount int) error {
	const q = `
		UPDATE seed_runs
		SET status = 'collecting', sent_count = $2,
		    started_at = COALESCE(started_at, now()), updated_at = now()
		WHERE id = $1`
	if _, err := s.Pool.Exec(ctx, q, id, sentCount); err != nil {
		return fmt.Errorf("mark seed run collecting: %w", err)
	}
	return nil
}

// MarkSeedRunCompleted sets status=completed and stamps completed_at.
func (s *Store) MarkSeedRunCompleted(ctx context.Context, id string) error {
	const q = `UPDATE seed_runs SET status = 'completed', completed_at = now(), updated_at = now() WHERE id = $1`
	if _, err := s.Pool.Exec(ctx, q, id); err != nil {
		return fmt.Errorf("mark seed run completed: %w", err)
	}
	return nil
}

// SeedResultStatusCounts returns a status -> count map for a run's results, so seedd can decide
// state transitions without loading every row.
func (s *Store) SeedResultStatusCounts(ctx context.Context, runID string) (map[string]int, error) {
	const q = `SELECT status, count(*) FROM seed_results WHERE run_id = $1 GROUP BY status`
	rows, err := s.Pool.Query(ctx, q, runID)
	if err != nil {
		return nil, fmt.Errorf("seed result status counts: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return nil, fmt.Errorf("scan status count: %w", err)
		}
		out[status] = n
	}
	return out, rows.Err()
}
