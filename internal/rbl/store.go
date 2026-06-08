package rbl

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
)

// Store reads and writes the ip_blocklist_status table via the shared postgres pool. It
// lives in the rbl package (per the parallel-safety rules) and uses the exported
// (*postgres.Store).Pool handle rather than adding files under internal/store/postgres.
type Store struct {
	pg *pgstore.Store
}

// NewStore wraps a *postgres.Store.
func NewStore(pg *pgstore.Store) *Store { return &Store{pg: pg} }

// Row is one (ip, zone) blocklist-status row.
type Row struct {
	IP          string
	Zone        string
	Listed      bool
	Reason      string
	CheckedAt   time.Time
	ListedSince *time.Time
}

// Upsert writes one Listing result and returns transition=true with newlyListed indicating
// the direction, so the caller can open an incident only on a clean->listed edge.
//
// listed_since semantics:
//   - clean -> listed: stamp listed_since = now (checked_at).
//   - listed -> listed: keep the existing listed_since.
//   - listed -> clean: clear listed_since (NULL).
//   - clean -> clean:  listed_since stays NULL.
//
// It is implemented as a single INSERT ... ON CONFLICT so concurrent ticks are safe, and it
// RETURNs whether the listing flag changed and its previous value.
func (s *Store) Upsert(ctx context.Context, l Listing, checkedAt time.Time) (transition, nowListed bool, err error) {
	// Read the PREVIOUS listed value first so we can detect a clean<->listed transition.
	// rbld runs on a single-node ticker, so there is no concurrent writer to race with.
	prevListed, prevExists, err := s.previousListed(ctx, l.IP, l.Zone)
	if err != nil {
		return false, false, err
	}

	const write = `
INSERT INTO ip_blocklist_status (ip, zone, listed, reason, checked_at, listed_since)
VALUES ($1, $2, $3, NULLIF($4,''), $5, CASE WHEN $3 THEN $5 ELSE NULL END)
ON CONFLICT (ip, zone) DO UPDATE SET
    listed       = EXCLUDED.listed,
    reason       = CASE WHEN EXCLUDED.listed THEN EXCLUDED.reason ELSE NULL END,
    checked_at   = EXCLUDED.checked_at,
    listed_since = CASE
        WHEN EXCLUDED.listed AND NOT ip_blocklist_status.listed THEN EXCLUDED.checked_at
        WHEN EXCLUDED.listed AND ip_blocklist_status.listed THEN ip_blocklist_status.listed_since
        ELSE NULL
    END`
	if _, err := s.pg.Pool.Exec(ctx, write, l.IP, l.Zone, l.Listed, l.Reason, checkedAt); err != nil {
		return false, false, fmt.Errorf("upsert ip_blocklist_status %s/%s: %w", l.IP, l.Zone, err)
	}

	// A meaningful transition is a change in the listed flag. A brand-new row that is listed
	// is also a clean->listed edge (no prior clean state recorded, but the IP is newly seen
	// as listed and warrants an incident).
	if !prevExists {
		return l.Listed, l.Listed, nil
	}
	return prevListed != l.Listed, l.Listed, nil
}

func (s *Store) previousListed(ctx context.Context, ip, zone string) (listed, exists bool, err error) {
	const q = `SELECT listed FROM ip_blocklist_status WHERE ip = $1 AND zone = $2`
	err = s.pg.Pool.QueryRow(ctx, q, ip, zone).Scan(&listed)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, false, nil // no prior state recorded for this (ip, zone)
	}
	if err != nil {
		return false, false, fmt.Errorf("read previous listed %s/%s: %w", ip, zone, err)
	}
	return listed, true, nil
}

// PruneStale removes rows for (ip, zone) pairs that are no longer in the active set, so the
// status view doesn't accumulate IPs/zones removed from config. Pass the currently-configured
// IPs and zones. A no-op when either slice is empty (avoids wiping everything on misconfig).
func (s *Store) PruneStale(ctx context.Context, ips, zones []string) error {
	if len(ips) == 0 || len(zones) == 0 {
		return nil
	}
	const q = `DELETE FROM ip_blocklist_status WHERE NOT (ip = ANY($1) AND zone = ANY($2))`
	if _, err := s.pg.Pool.Exec(ctx, q, ips, zones); err != nil {
		return fmt.Errorf("prune stale ip_blocklist_status: %w", err)
	}
	return nil
}

// List returns all status rows, ordered by ip then zone.
func (s *Store) List(ctx context.Context) ([]Row, error) {
	const q = `SELECT ip, zone, listed, COALESCE(reason, ''), checked_at, listed_since
	           FROM ip_blocklist_status ORDER BY ip, zone`
	rows, err := s.pg.Pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list ip_blocklist_status: %w", err)
	}
	defer rows.Close()

	var out []Row
	for rows.Next() {
		var r Row
		if err := rows.Scan(&r.IP, &r.Zone, &r.Listed, &r.Reason, &r.CheckedAt, &r.ListedSince); err != nil {
			return nil, fmt.Errorf("scan ip_blocklist_status: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// HealthyIPs returns the set of egress IPs that are listed on ZERO zones, restricted to the
// supplied configured IP set. An IP with no rows yet (never checked) is NOT considered
// healthy — it's unknown — so it is excluded until at least one clean result is recorded.
func (s *Store) HealthyIPs(ctx context.Context, configured []string) ([]string, error) {
	rows, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	configuredSet := make(map[string]struct{}, len(configured))
	for _, ip := range configured {
		configuredSet[ip] = struct{}{}
	}
	checked := make(map[string]bool) // ip -> seen at least one row
	listed := make(map[string]bool)  // ip -> listed on >=1 zone
	for _, r := range rows {
		if _, ok := configuredSet[r.IP]; !ok {
			continue
		}
		checked[r.IP] = true
		if r.Listed {
			listed[r.IP] = true
		}
	}
	var out []string
	for ip := range checked {
		if !listed[ip] {
			out = append(out, ip)
		}
	}
	sort.Strings(out)
	return out, nil
}
