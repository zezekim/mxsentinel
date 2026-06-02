package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

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

// DNSChangeRow is a snapshot (with its findings) used by the correlation engine.
type DNSChangeRow struct {
	SnapshotID string
	CapturedAt time.Time
	Findings   []contracts.DNSFinding
}

// RecentDNSChanges returns a tenant domain's snapshots captured since `since`, newest
// first, each with its findings — input for correlating rejection spikes with DNS changes.
func (s *Store) RecentDNSChanges(ctx context.Context, tenantID, domainName string, since time.Time) ([]DNSChangeRow, error) {
	const q = `SELECT s.id, s.captured_at,
	                  f.category::text, f.severity::text, f.code, f.message, f.detail
	           FROM dns_snapshots s
	           JOIN domains d ON d.id = s.domain_id
	           LEFT JOIN dns_findings f ON f.snapshot_id = s.id
	           WHERE d.tenant_id = $1 AND d.name = $2 AND s.captured_at >= $3
	           ORDER BY s.captured_at DESC, s.id`
	rows, err := s.Pool.Query(ctx, q, tenantID, domainName, since)
	if err != nil {
		return nil, fmt.Errorf("recent dns changes: %w", err)
	}
	defer rows.Close()

	var out []DNSChangeRow
	byID := map[string]int{} // snapshot id -> index in out
	for rows.Next() {
		var (
			id                  string
			captured            time.Time
			cat, sev, code, msg *string
			detail              []byte
		)
		if err := rows.Scan(&id, &captured, &cat, &sev, &code, &msg, &detail); err != nil {
			return nil, fmt.Errorf("scan dns change: %w", err)
		}
		idx, ok := byID[id]
		if !ok {
			out = append(out, DNSChangeRow{SnapshotID: id, CapturedAt: captured})
			idx = len(out) - 1
			byID[id] = idx
		}
		if cat != nil { // LEFT JOIN: nil when the snapshot had no findings
			f := contracts.DNSFinding{Category: *cat, Severity: deref(sev), Code: deref(code), Message: deref(msg)}
			if len(detail) > 0 {
				_ = json.Unmarshal(detail, &f.Detail)
			}
			out[idx].Findings = append(out[idx].Findings, f)
		}
	}
	return out, rows.Err()
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// MarkDomainChecked stamps a domain's last_checked_at, called after every poll regardless
// of whether the state changed.
func (s *Store) MarkDomainChecked(ctx context.Context, domainID string) error {
	const q = `UPDATE domains SET last_checked_at = now() WHERE id = $1`
	_, err := s.Pool.Exec(ctx, q, domainID)
	return err
}
