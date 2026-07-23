package clickhouse

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// SMTPProbeRow is one synthetic-probe result written to the high-frequency
// smtp_probe_results table in ClickHouse. It is the time-series backing for latency/uptime
// history charts. Relay-wide (no tenant_id), mirroring the probe's infrastructure scope.
type SMTPProbeRow struct {
	ProbedAt      time.Time
	Endpoint      string
	Host          string
	Port          uint16
	Mode          string
	OK            uint8
	Stage         string
	Error         string
	LatencyMS     uint32
	TLSNegotiated uint8
	TLSVersion    string
	TLSChainValid uint8
	CertDaysLeft  int32
	CertNotAfter  time.Time
	CertExpiring  uint8
	Greylisting   uint8
	ResponseCode  uint16
}

const smtpProbeInsertStmt = `INSERT INTO smtp_probe_results (
	probed_at, endpoint, host, port, mode, ok, stage, error, latency_ms,
	tls_negotiated, tls_version, tls_chain_valid, cert_days_until_expiry,
	cert_not_after, cert_expiring, greylisting, response_code
)`

// InsertSMTPProbeResults batch-writes probe rows.
func (s *Store) InsertSMTPProbeResults(ctx context.Context, rows []SMTPProbeRow) error {
	if len(rows) == 0 {
		return nil
	}
	batch, err := s.conn.PrepareBatch(ctx, smtpProbeInsertStmt)
	if err != nil {
		return fmt.Errorf("prepare smtp_probe_results batch: %w", err)
	}
	for i := range rows {
		r := &rows[i]
		notAfter := r.CertNotAfter
		if notAfter.IsZero() {
			notAfter = time.Unix(0, 0).UTC()
		}
		if err := batch.Append(
			r.ProbedAt, r.Endpoint, r.Host, r.Port, r.Mode, r.OK, r.Stage, r.Error, r.LatencyMS,
			r.TLSNegotiated, r.TLSVersion, r.TLSChainValid, r.CertDaysLeft,
			notAfter, r.CertExpiring, r.Greylisting, r.ResponseCode,
		); err != nil {
			return fmt.Errorf("append smtp_probe row %d: %w", i, err)
		}
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("send smtp_probe_results batch: %w", err)
	}
	return nil
}

// SMTPProbePoint is one historical probe observation for charting.
type SMTPProbePoint struct {
	ProbedAt     time.Time `json:"probed_at"`
	Endpoint     string    `json:"endpoint"`
	OK           bool      `json:"ok"`
	LatencyMS    uint32    `json:"latency_ms"`
	Stage        string    `json:"stage,omitempty"`
	CertDaysLeft int32     `json:"cert_days_until_expiry"`
}

// SMTPProbeHistory returns per-probe observations for latency/uptime charts, newest first.
// endpoint filters to one "host:port" (empty = all); since/until bound the window (zero =
// unbounded on that side); limit caps rows (default 1000).
func (s *Store) SMTPProbeHistory(ctx context.Context, endpoint string, since, until time.Time, limit int) ([]SMTPProbePoint, error) {
	if limit <= 0 || limit > 20000 {
		limit = 1000
	}
	var sb strings.Builder
	sb.WriteString(`SELECT probed_at, endpoint, ok, latency_ms, stage, cert_days_until_expiry
		FROM smtp_probe_results WHERE 1=1`)
	args := []any{}
	if endpoint != "" {
		sb.WriteString(" AND endpoint = ?")
		args = append(args, endpoint)
	}
	if !since.IsZero() {
		sb.WriteString(" AND probed_at >= ?")
		args = append(args, since)
	}
	if !until.IsZero() {
		sb.WriteString(" AND probed_at <= ?")
		args = append(args, until)
	}
	sb.WriteString(" ORDER BY probed_at DESC LIMIT ?")
	args = append(args, limit)

	rows, err := s.conn.Query(ctx, sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("smtp_probe history: %w", err)
	}
	defer rows.Close()

	var out []SMTPProbePoint
	for rows.Next() {
		var p SMTPProbePoint
		var ok uint8
		if err := rows.Scan(&p.ProbedAt, &p.Endpoint, &ok, &p.LatencyMS, &p.Stage, &p.CertDaysLeft); err != nil {
			return nil, fmt.Errorf("scan smtp_probe history: %w", err)
		}
		p.OK = ok == 1
		out = append(out, p)
	}
	return out, rows.Err()
}

// SMTPProbeUptime is an aggregate uptime/latency rollup for one endpoint over a window.
type SMTPProbeUptime struct {
	Endpoint   string  `json:"endpoint"`
	Total      uint64  `json:"total"`
	OKCount    uint64  `json:"ok_count"`
	UptimePct  float64 `json:"uptime_pct"`
	AvgLatency float64 `json:"avg_latency_ms"`
	P95Latency float64 `json:"p95_latency_ms"`
	MaxLatency uint32  `json:"max_latency_ms"`
}

// SMTPProbeUptimeByEndpoint rolls up uptime and latency per endpoint over the given window.
func (s *Store) SMTPProbeUptimeByEndpoint(ctx context.Context, since, until time.Time) ([]SMTPProbeUptime, error) {
	var sb strings.Builder
	sb.WriteString(`SELECT endpoint, count() AS total, countIf(ok = 1) AS ok_count,
		avg(latency_ms) AS avg_latency, quantile(0.95)(latency_ms) AS p95, max(latency_ms) AS max_latency
		FROM smtp_probe_results WHERE 1=1`)
	args := []any{}
	if !since.IsZero() {
		sb.WriteString(" AND probed_at >= ?")
		args = append(args, since)
	}
	if !until.IsZero() {
		sb.WriteString(" AND probed_at <= ?")
		args = append(args, until)
	}
	sb.WriteString(" GROUP BY endpoint ORDER BY endpoint")

	rows, err := s.conn.Query(ctx, sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("smtp_probe uptime: %w", err)
	}
	defer rows.Close()

	var out []SMTPProbeUptime
	for rows.Next() {
		var u SMTPProbeUptime
		if err := rows.Scan(&u.Endpoint, &u.Total, &u.OKCount, &u.AvgLatency, &u.P95Latency, &u.MaxLatency); err != nil {
			return nil, fmt.Errorf("scan smtp_probe uptime: %w", err)
		}
		if u.Total > 0 {
			u.UptimePct = float64(u.OKCount) / float64(u.Total) * 100
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
