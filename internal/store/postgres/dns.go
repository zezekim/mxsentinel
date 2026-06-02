package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/zezekim/mxsentinel/pkg/contracts"
)

// MonitoredDomain is a domain the DNS validator should poll.
type MonitoredDomain struct {
	ID                string
	TenantID          string
	Name              string
	CheckIntervalSecs int
}

// ListMonitoredDomains returns domains that are not paused, for the DNS scheduler.
func (s *Store) ListMonitoredDomains(ctx context.Context) ([]MonitoredDomain, error) {
	const q = `SELECT id, tenant_id, name, check_interval_secs
	           FROM domains WHERE status <> 'paused' ORDER BY name`
	rows, err := s.Pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list monitored domains: %w", err)
	}
	defer rows.Close()

	var out []MonitoredDomain
	for rows.Next() {
		var d MonitoredDomain
		if err := rows.Scan(&d.ID, &d.TenantID, &d.Name, &d.CheckIntervalSecs); err != nil {
			return nil, fmt.Errorf("scan monitored domain: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// LatestSnapshotChecksum returns the checksum of a domain's most recent snapshot, or ""
// if there is none.
func (s *Store) LatestSnapshotChecksum(ctx context.Context, domainID string) (string, error) {
	const q = `SELECT checksum FROM dns_snapshots WHERE domain_id = $1
	           ORDER BY captured_at DESC LIMIT 1`
	var sum string
	err := s.Pool.QueryRow(ctx, q, domainID).Scan(&sum)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("latest snapshot checksum: %w", err)
	}
	return sum, nil
}

// InsertSnapshot writes a snapshot and its findings in one transaction and returns the new
// snapshot id.
func (s *Store) InsertSnapshot(ctx context.Context, domainID string, stateJSON []byte, checksum string, healthy bool, findings []contracts.DNSFinding) (string, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const insSnap = `INSERT INTO dns_snapshots (domain_id, state, checksum, is_healthy)
	                 VALUES ($1, $2, $3, $4) RETURNING id`
	var snapID string
	if err := tx.QueryRow(ctx, insSnap, domainID, stateJSON, checksum, healthy).Scan(&snapID); err != nil {
		return "", fmt.Errorf("insert snapshot: %w", err)
	}

	const insFinding = `INSERT INTO dns_findings
	    (snapshot_id, domain_id, category, severity, code, message, detail)
	    VALUES ($1, $2, $3::dns_category, $4::dns_finding_sev, $5, $6, $7)`
	for _, f := range findings {
		detail := []byte("{}")
		if f.Detail != nil {
			if b, err := json.Marshal(f.Detail); err == nil {
				detail = b
			}
		}
		if _, err := tx.Exec(ctx, insFinding, snapID, domainID, f.Category, f.Severity, f.Code, f.Message, detail); err != nil {
			return "", fmt.Errorf("insert finding %s: %w", f.Code, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit snapshot: %w", err)
	}
	return snapID, nil
}

// MarkDomainChecked stamps a domain's last_checked_at, called after every poll regardless
// of whether the state changed.
func (s *Store) MarkDomainChecked(ctx context.Context, domainID string) error {
	const q = `UPDATE domains SET last_checked_at = now() WHERE id = $1`
	_, err := s.Pool.Exec(ctx, q, domainID)
	return err
}
