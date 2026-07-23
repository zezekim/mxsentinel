package postgres

import (
	"context"
	"fmt"
	"time"
)

// SMTPProbeResult is one recorded synthetic-probe outcome for a relay endpoint. It is
// relay-wide infrastructure state (no tenant_id), mirroring ip_blocklist_status. The
// smtpprobe package owns the richer in-memory type; this flat row is what persists.
type SMTPProbeResult struct {
	ID              string
	Endpoint        string // "host:port"
	Host            string
	Port            int
	Mode            string
	OK              bool
	Stage           string
	Error           string
	LatencyMS       int64
	Banner          string
	STARTTLSOffered bool
	AuthAdvertised  bool
	AuthMechs       []string
	TLSNegotiated   bool
	TLSVersion      string
	TLSCipher       string
	TLSChainValid   bool
	CertSubject     string
	CertIssuer      string
	CertNotAfter    *time.Time
	CertDaysLeft    *int
	CertExpiring    bool
	Greylisting     bool
	ResponseCode    int
	ProbedAt        time.Time
}

// InsertSMTPProbeResult appends one probe result to smtp_probe_results.
func (s *Store) InsertSMTPProbeResult(ctx context.Context, r SMTPProbeResult) error {
	const q = `
		INSERT INTO smtp_probe_results
			(endpoint, host, port, mode, ok, stage, error, latency_ms, banner,
			 starttls_offered, auth_advertised, auth_mechs,
			 tls_negotiated, tls_version, tls_cipher, tls_chain_valid,
			 cert_subject, cert_issuer, cert_not_after, cert_days_until_expiry, cert_expiring,
			 greylisting, response_code, probed_at)
		VALUES
			($1,$2,$3,$4,$5,$6,NULLIF($7,''),$8,NULLIF($9,''),
			 $10,$11,$12,
			 $13,NULLIF($14,''),NULLIF($15,''),$16,
			 NULLIF($17,''),NULLIF($18,''),$19,$20,$21,
			 $22,$23,$24)`
	if _, err := s.Pool.Exec(ctx, q,
		r.Endpoint, r.Host, r.Port, r.Mode, r.OK, r.Stage, r.Error, r.LatencyMS, r.Banner,
		r.STARTTLSOffered, r.AuthAdvertised, r.AuthMechs,
		r.TLSNegotiated, r.TLSVersion, r.TLSCipher, r.TLSChainValid,
		r.CertSubject, r.CertIssuer, r.CertNotAfter, r.CertDaysLeft, r.CertExpiring,
		r.Greylisting, r.ResponseCode, r.ProbedAt,
	); err != nil {
		return fmt.Errorf("insert smtp_probe_result %s: %w", r.Endpoint, err)
	}
	return nil
}

// LatestSMTPProbeResults returns the most recent result per endpoint, newest first. This is
// the current-status view backing GET /v1/smtp-probes.
func (s *Store) LatestSMTPProbeResults(ctx context.Context) ([]SMTPProbeResult, error) {
	const q = `
		SELECT DISTINCT ON (endpoint)
		       id, endpoint, host, port, mode, ok, COALESCE(stage,''), COALESCE(error,''),
		       latency_ms, COALESCE(banner,''), starttls_offered, auth_advertised,
		       COALESCE(auth_mechs, '{}'), tls_negotiated, COALESCE(tls_version,''),
		       COALESCE(tls_cipher,''), tls_chain_valid, COALESCE(cert_subject,''),
		       COALESCE(cert_issuer,''), cert_not_after, cert_days_until_expiry, cert_expiring,
		       greylisting, response_code, probed_at
		FROM smtp_probe_results
		ORDER BY endpoint, probed_at DESC`
	rows, err := s.Pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("latest smtp_probe_results: %w", err)
	}
	defer rows.Close()
	return scanProbeRows(rows)
}

// SMTPProbeHistory returns recent probe rows for one endpoint (or all endpoints when
// endpoint is empty), newest first, capped at limit. Used as a Postgres-backed fallback for
// the history view; the high-frequency store is ClickHouse.
func (s *Store) SMTPProbeHistory(ctx context.Context, endpoint string, since time.Time, limit int) ([]SMTPProbeResult, error) {
	if limit <= 0 || limit > 5000 {
		limit = 500
	}
	const q = `
		SELECT id, endpoint, host, port, mode, ok, COALESCE(stage,''), COALESCE(error,''),
		       latency_ms, COALESCE(banner,''), starttls_offered, auth_advertised,
		       COALESCE(auth_mechs, '{}'), tls_negotiated, COALESCE(tls_version,''),
		       COALESCE(tls_cipher,''), tls_chain_valid, COALESCE(cert_subject,''),
		       COALESCE(cert_issuer,''), cert_not_after, cert_days_until_expiry, cert_expiring,
		       greylisting, response_code, probed_at
		FROM smtp_probe_results
		WHERE ($1 = '' OR endpoint = $1)
		  AND ($2::timestamptz IS NULL OR probed_at >= $2)
		ORDER BY probed_at DESC
		LIMIT $3`
	var sincePtr *time.Time
	if !since.IsZero() {
		sincePtr = &since
	}
	rows, err := s.Pool.Query(ctx, q, endpoint, sincePtr, limit)
	if err != nil {
		return nil, fmt.Errorf("smtp_probe history: %w", err)
	}
	defer rows.Close()
	return scanProbeRows(rows)
}

func scanProbeRows(rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}) ([]SMTPProbeResult, error) {
	var out []SMTPProbeResult
	for rows.Next() {
		var r SMTPProbeResult
		if err := rows.Scan(
			&r.ID, &r.Endpoint, &r.Host, &r.Port, &r.Mode, &r.OK, &r.Stage, &r.Error,
			&r.LatencyMS, &r.Banner, &r.STARTTLSOffered, &r.AuthAdvertised,
			&r.AuthMechs, &r.TLSNegotiated, &r.TLSVersion,
			&r.TLSCipher, &r.TLSChainValid, &r.CertSubject,
			&r.CertIssuer, &r.CertNotAfter, &r.CertDaysLeft, &r.CertExpiring,
			&r.Greylisting, &r.ResponseCode, &r.ProbedAt,
		); err != nil {
			return nil, fmt.Errorf("scan smtp_probe_result: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpsertSMTPProbeTarget records a configured probe target so the API can show the endpoint
// universe even before the first result lands (optional; probed calls it on start).
func (s *Store) UpsertSMTPProbeTarget(ctx context.Context, endpoint, host string, port int, mode string) error {
	const q = `
		INSERT INTO smtp_probe_targets (endpoint, host, port, mode, enabled, updated_at)
		VALUES ($1,$2,$3,$4,TRUE,now())
		ON CONFLICT (endpoint) DO UPDATE SET
			host = EXCLUDED.host, port = EXCLUDED.port, mode = EXCLUDED.mode,
			enabled = TRUE, updated_at = now()`
	if _, err := s.Pool.Exec(ctx, q, endpoint, host, port, mode); err != nil {
		return fmt.Errorf("upsert smtp_probe_target %s: %w", endpoint, err)
	}
	return nil
}

// SMTPProbeTarget is a configured probe endpoint stored in Postgres.
type SMTPProbeTarget struct {
	Endpoint string
	Host     string
	Port     int
	Mode     string
	Enabled  bool
}

// ListSMTPProbeTargets returns enabled configured targets.
func (s *Store) ListSMTPProbeTargets(ctx context.Context) ([]SMTPProbeTarget, error) {
	const q = `SELECT endpoint, host, port, mode, enabled FROM smtp_probe_targets
	           WHERE enabled ORDER BY host, port`
	rows, err := s.Pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list smtp_probe_targets: %w", err)
	}
	defer rows.Close()
	var out []SMTPProbeTarget
	for rows.Next() {
		var t SMTPProbeTarget
		if err := rows.Scan(&t.Endpoint, &t.Host, &t.Port, &t.Mode, &t.Enabled); err != nil {
			return nil, fmt.Errorf("scan smtp_probe_target: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
