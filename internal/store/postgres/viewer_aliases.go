package postgres

import (
	"context"
	"fmt"
)

// ViewerAlias is a provider-owned hostname and the sequence number that names its
// viewer-facing alias (see migrations/postgres/00029_viewer_domain_aliases.sql).
type ViewerAlias struct {
	RealHost string
	Seq      int
}

// ListViewerAliases returns every assigned hostname alias. The masker loads these once at
// startup and keeps them in memory; the set is small (one row per provider hostname ever
// observed) and only grows when a new hostname first appears in a response.
func (s *Store) ListViewerAliases(ctx context.Context) ([]ViewerAlias, error) {
	const q = `SELECT real_host, seq FROM viewer_domain_aliases ORDER BY seq`
	rows, err := s.Pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list viewer aliases: %w", err)
	}
	defer rows.Close()

	var out []ViewerAlias
	for rows.Next() {
		var a ViewerAlias
		if err := rows.Scan(&a.RealHost, &a.Seq); err != nil {
			return nil, fmt.Errorf("scan viewer alias: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AssignViewerAlias returns the sequence number for host, allocating one if this is the
// first sighting. Concurrent callers racing on the same host converge on one row: the
// insert is a no-op on conflict and the follow-up select reads whichever row won.
func (s *Store) AssignViewerAlias(ctx context.Context, host string) (int, error) {
	const q = `INSERT INTO viewer_domain_aliases (real_host) VALUES ($1)
	           ON CONFLICT (real_host) DO NOTHING`
	if _, err := s.Pool.Exec(ctx, q, host); err != nil {
		return 0, fmt.Errorf("assign viewer alias: %w", err)
	}
	const sel = `SELECT seq FROM viewer_domain_aliases WHERE real_host = $1`
	var seq int
	if err := s.Pool.QueryRow(ctx, sel, host).Scan(&seq); err != nil {
		return 0, fmt.Errorf("read viewer alias: %w", err)
	}
	return seq, nil
}
