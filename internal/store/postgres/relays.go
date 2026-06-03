package postgres

import (
	"context"
	"fmt"
	"strings"
)

// CreateIPPool registers a sending IP pool (a set of outbound IPs with a reputation
// purpose) and returns its id. addresses are IPv4/IPv6 strings.
func (s *Store) CreateIPPool(ctx context.Context, tenantID, name, purpose string, addresses []string) (string, error) {
	// Pass the addresses as a Postgres array literal and cast text -> inet[].
	arr := "{" + strings.Join(addresses, ",") + "}"
	const q = `INSERT INTO ip_pools (tenant_id, name, purpose, addresses)
	           VALUES ($1, $2, $3::ip_pool_purpose, $4::inet[])
	           RETURNING id`
	var id string
	if err := s.Pool.QueryRow(ctx, q, tenantID, name, purpose, arr).Scan(&id); err != nil {
		return "", fmt.Errorf("create ip pool: %w", err)
	}
	return id, nil
}

// CreateRelayNode registers a relay host (its primary outbound IP + software) so the
// reputation monitor and correlation engine can attribute telemetry to it. Empty
// tenantID/primaryIP/software store NULL.
func (s *Store) CreateRelayNode(ctx context.Context, tenantID, hostname, primaryIP, software string) (string, error) {
	const q = `INSERT INTO relay_nodes (tenant_id, hostname, primary_ip, software)
	           VALUES (NULLIF($1,'')::uuid, $2, NULLIF($3,'')::inet, NULLIF($4,''))
	           RETURNING id`
	var id string
	if err := s.Pool.QueryRow(ctx, q, tenantID, hostname, primaryIP, software).Scan(&id); err != nil {
		return "", fmt.Errorf("create relay node: %w", err)
	}
	return id, nil
}
