package clickhouse

import (
	"context"
	"fmt"
	"time"
)

// SeedPlacementRow is one finalized seed result appended to seed_placement_results. seedd
// writes these once a result reaches a terminal placement, for long-horizon trend analytics.
// (migrations/clickhouse/00007_seed_placement.sql).
type SeedPlacementRow struct {
	ResultID   string
	RunID      string
	TenantID   string
	ListID     string // "" when the run was not tied to a stored list
	RunTag     string
	Address    string
	Provider   string
	IPPool     string
	Placement  string // unknown | inbox | spam | missing
	SPFPass    uint8
	DKIMPass   uint8
	DMARCPass  uint8
	SentAt     time.Time
	ObservedAt time.Time
}

const seedPlacementInsertStmt = `INSERT INTO seed_placement_results (
	result_id, run_id, tenant_id, list_id, run_tag, address, provider, ip_pool,
	placement, spf_pass, dkim_pass, dmarc_pass, sent_at, observed_at, ingested_at
)`

// nilUUID is the all-zero UUID used when a nullable UUID column (list_id) has no value.
const nilUUID = "00000000-0000-0000-0000-000000000000"

// InsertSeedPlacements writes a batch of finalized seed placement rows.
func (s *Store) InsertSeedPlacements(ctx context.Context, rows []SeedPlacementRow) error {
	if len(rows) == 0 {
		return nil
	}
	batch, err := s.conn.PrepareBatch(ctx, seedPlacementInsertStmt)
	if err != nil {
		return fmt.Errorf("prepare seed_placement batch: %w", err)
	}
	now := time.Now().UTC()
	for i := range rows {
		r := &rows[i]
		listID := r.ListID
		if listID == "" {
			listID = nilUUID
		}
		if err := batch.Append(
			r.ResultID, r.RunID, r.TenantID, listID, r.RunTag, r.Address, r.Provider, r.IPPool,
			r.Placement, r.SPFPass, r.DKIMPass, r.DMARCPass, r.SentAt, r.ObservedAt, now,
		); err != nil {
			return fmt.Errorf("append seed_placement row %d: %w", i, err)
		}
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("send seed_placement batch: %w", err)
	}
	return nil
}

// SeedPlacementTrend is one provider's aggregated placement over a query window.
type SeedPlacementTrend struct {
	Provider string
	Total    uint64
	Inbox    uint64
	Spam     uint64
	Missing  uint64
}

// PlacementByProvider aggregates historical seed placement per provider for a tenant since a
// given time — the read path for the long-term inbox-rate trend view.
func (s *Store) PlacementByProvider(ctx context.Context, tenantID string, since time.Time) ([]SeedPlacementTrend, error) {
	const q = `
		SELECT provider,
		       count(),
		       countIf(placement = 'inbox'),
		       countIf(placement = 'spam'),
		       countIf(placement = 'missing')
		FROM seed_placement_results
		WHERE tenant_id = ? AND observed_at >= ?
		GROUP BY provider
		ORDER BY count() DESC`
	rows, err := s.conn.Query(ctx, q, tenantID, since)
	if err != nil {
		return nil, fmt.Errorf("placement by provider: %w", err)
	}
	defer rows.Close()

	var out []SeedPlacementTrend
	for rows.Next() {
		var t SeedPlacementTrend
		if err := rows.Scan(&t.Provider, &t.Total, &t.Inbox, &t.Spam, &t.Missing); err != nil {
			return nil, fmt.Errorf("scan placement trend: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
