package postgres

import (
	"context"
	"fmt"
)

// ReputationTarget is an IP to check against DNSBLs, with its owning tenant and a label.
type ReputationTarget struct {
	TenantID string
	IP       string
	Label    string // relay hostname or IP-pool name
	Source   string // "relay" | "pool"
}

// ListReputationTargets returns every tenant-attributable sending IP from relay_nodes and
// ip_pools, for the reputation checker to scan.
func (s *Store) ListReputationTargets(ctx context.Context) ([]ReputationTarget, error) {
	const q = `
		SELECT tenant_id::text, host(primary_ip), hostname, 'relay'
		FROM relay_nodes
		WHERE primary_ip IS NOT NULL AND tenant_id IS NOT NULL
		UNION ALL
		SELECT tenant_id::text, host(addr), name, 'pool'
		FROM ip_pools, unnest(addresses) AS addr`
	rows, err := s.Pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list reputation targets: %w", err)
	}
	defer rows.Close()

	var out []ReputationTarget
	for rows.Next() {
		var t ReputationTarget
		if err := rows.Scan(&t.TenantID, &t.IP, &t.Label, &t.Source); err != nil {
			return nil, fmt.Errorf("scan reputation target: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
