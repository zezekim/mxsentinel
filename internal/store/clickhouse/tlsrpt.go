package clickhouse

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"
)

// TLSRPTResultRow is one row for tlsrpt_results. The tlsrpt ingestor builds these from
// parsed TLS-RPT JSON reports: one summary row per policy (result_type "successful") plus
// one row per failure detail. IP columns take net.IP (nil is written as :: / all-zero).
type TLSRPTResultRow struct {
	ReportID            string
	OrgName             string
	TenantID            string // UUID string
	PolicyDomain        string
	PolicyType          string
	DateBegin           time.Time
	DateEnd             time.Time
	ResultType          string
	SendingMTAIP        net.IP
	ReceivingMXHostname string
	ReceivingIP         net.IP
	SuccessCount        uint64
	FailureCount        uint64
	IngestedAt          time.Time
}

const tlsrptInsertStmt = `INSERT INTO tlsrpt_results (report_id, org_name, tenant_id, policy_domain, policy_type, date_begin, date_end, result_type, sending_mta_ip, receiving_mx_hostname, receiving_ip, success_count, failure_count, ingested_at)`

// InsertTLSRPTResults writes a batch of parsed TLS-RPT rows.
func (s *Store) InsertTLSRPTResults(ctx context.Context, rows []TLSRPTResultRow) error {
	if len(rows) == 0 {
		return nil
	}
	batch, err := s.conn.PrepareBatch(ctx, tlsrptInsertStmt)
	if err != nil {
		return fmt.Errorf("prepare tlsrpt_results batch: %w", err)
	}
	for i := range rows {
		r := &rows[i]
		if err := batch.Append(
			r.ReportID, r.OrgName, r.TenantID, r.PolicyDomain, r.PolicyType,
			r.DateBegin, r.DateEnd, r.ResultType,
			ipOrZero(r.SendingMTAIP), r.ReceivingMXHostname, ipOrZero(r.ReceivingIP),
			r.SuccessCount, r.FailureCount, r.IngestedAt,
		); err != nil {
			return fmt.Errorf("append row %d: %w", i, err)
		}
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("send tlsrpt_results batch: %w", err)
	}
	return nil
}

// TLSRPTSummary holds aggregate TLS session outcome counts for a tenant.
type TLSRPTSummary struct {
	Success uint64
	Failure uint64
}

// TLSRPTSummaryFor returns aggregate successful/failed TLS session counts from
// tlsrpt_results (optionally scoped to a policy domain and time window).
func (s *Store) TLSRPTSummaryFor(ctx context.Context, tenantID, domain string, since, until time.Time) (TLSRPTSummary, error) {
	var sb strings.Builder
	sb.WriteString(`SELECT sum(success_count), sum(failure_count) FROM tlsrpt_results WHERE tenant_id = ?`)
	args := []any{tenantID}
	if domain != "" {
		sb.WriteString(" AND policy_domain = ?")
		args = append(args, domain)
	}
	if !since.IsZero() {
		sb.WriteString(" AND date_begin >= ?")
		args = append(args, since)
	}
	if !until.IsZero() {
		sb.WriteString(" AND date_begin <= ?")
		args = append(args, until)
	}
	var sum TLSRPTSummary
	if err := s.conn.QueryRow(ctx, sb.String(), args...).Scan(&sum.Success, &sum.Failure); err != nil {
		return TLSRPTSummary{}, fmt.Errorf("tlsrpt summary: %w", err)
	}
	return sum, nil
}

// TLSRPTFailureByType is a failure count grouped by result type.
type TLSRPTFailureByType struct {
	ResultType string
	Failures   uint64
}

// TLSRPTFailuresByType returns failure session counts grouped by result type for a tenant.
func (s *Store) TLSRPTFailuresByType(ctx context.Context, tenantID, domain string, since, until time.Time) ([]TLSRPTFailureByType, error) {
	var sb strings.Builder
	sb.WriteString(`SELECT result_type, sum(failure_count) AS f FROM tlsrpt_results WHERE tenant_id = ? AND failure_count > 0`)
	args := []any{tenantID}
	if domain != "" {
		sb.WriteString(" AND policy_domain = ?")
		args = append(args, domain)
	}
	if !since.IsZero() {
		sb.WriteString(" AND date_begin >= ?")
		args = append(args, since)
	}
	if !until.IsZero() {
		sb.WriteString(" AND date_begin <= ?")
		args = append(args, until)
	}
	sb.WriteString(" GROUP BY result_type ORDER BY f DESC")

	rows, err := s.conn.Query(ctx, sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("tlsrpt failures by type: %w", err)
	}
	defer rows.Close()

	var out []TLSRPTFailureByType
	for rows.Next() {
		var r TLSRPTFailureByType
		if err := rows.Scan(&r.ResultType, &r.Failures); err != nil {
			return nil, fmt.Errorf("scan tlsrpt failure row: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
