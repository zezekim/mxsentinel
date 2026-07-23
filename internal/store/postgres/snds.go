package postgres

import (
	"context"
	"fmt"
	"time"
)

// SNDSIPData is one (sending IP, day) SNDS reputation record. Mirrors the snds_ip_data table
// (migration 00020). It is the Microsoft counterpart to the fbl domain_reputation row.
type SNDSIPData struct {
	TenantID      string
	IP            string
	DataDate      time.Time
	RcptCount     int64
	DataCount     int64
	MsgRecipients int64
	FilterResult  string // GREEN | YELLOW | RED | ""
	ComplaintBand string
	TrapHits      int
	SampleHELO    string
	SampleFrom    string
	ActivityStart *time.Time
	ActivityEnd   *time.Time
	FetchedAt     time.Time
}

// UpsertSNDSIPData records (or refreshes) one SNDS per-IP-per-day row, tenant-scoped. The
// tenant is resolved by the daemon from the egress-IP inventory before calling.
func (s *Store) UpsertSNDSIPData(ctx context.Context, d SNDSIPData) error {
	const q = `
INSERT INTO snds_ip_data
    (tenant_id, ip, data_date, rcpt_count, data_count, message_recipients,
     filter_result, complaint_band, trap_hits, sample_helo, sample_from,
     activity_start, activity_end, fetched_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, now())
ON CONFLICT (tenant_id, ip, data_date) DO UPDATE SET
    rcpt_count         = EXCLUDED.rcpt_count,
    data_count         = EXCLUDED.data_count,
    message_recipients = EXCLUDED.message_recipients,
    filter_result      = EXCLUDED.filter_result,
    complaint_band     = EXCLUDED.complaint_band,
    trap_hits          = EXCLUDED.trap_hits,
    sample_helo        = EXCLUDED.sample_helo,
    sample_from        = EXCLUDED.sample_from,
    activity_start     = EXCLUDED.activity_start,
    activity_end       = EXCLUDED.activity_end,
    fetched_at         = now()`
	_, err := s.Pool.Exec(ctx, q,
		d.TenantID, d.IP, d.DataDate, d.RcptCount, d.DataCount, d.MsgRecipients,
		d.FilterResult, d.ComplaintBand, d.TrapHits, d.SampleHELO, d.SampleFrom,
		d.ActivityStart, d.ActivityEnd)
	if err != nil {
		return fmt.Errorf("upsert snds ip data: %w", err)
	}
	return nil
}

// SNDSLatestByIP returns the most recent SNDS row for each sending IP under a tenant (the
// "current filter state" view), worst filter result first. limit defaults to 100, capped 1000.
func (s *Store) SNDSLatestByIP(ctx context.Context, tenantID string, limit int) ([]SNDSIPData, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	const q = `
SELECT DISTINCT ON (ip)
       tenant_id::text, ip, data_date, rcpt_count, data_count, message_recipients,
       filter_result, complaint_band, trap_hits, sample_helo, sample_from,
       activity_start, activity_end, fetched_at
FROM snds_ip_data
WHERE tenant_id = $1
ORDER BY ip, data_date DESC
LIMIT $2`
	rows, err := s.Pool.Query(ctx, q, tenantID, limit)
	if err != nil {
		return nil, fmt.Errorf("snds latest by ip: %w", err)
	}
	defer rows.Close()

	var out []SNDSIPData
	for rows.Next() {
		var d SNDSIPData
		if err := rows.Scan(
			&d.TenantID, &d.IP, &d.DataDate, &d.RcptCount, &d.DataCount, &d.MsgRecipients,
			&d.FilterResult, &d.ComplaintBand, &d.TrapHits, &d.SampleHELO, &d.SampleFrom,
			&d.ActivityStart, &d.ActivityEnd, &d.FetchedAt,
		); err != nil {
			return nil, fmt.Errorf("scan snds row: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// SNDSTrendPoint is one day of a single IP's SNDS history for the trend sparkline.
type SNDSTrendPoint struct {
	DataDate     time.Time
	FilterResult string
	TrapHits     int
	RcptCount    int64
}

// SNDSIPTrend returns up to `days` most-recent daily rows for one IP, oldest-first, from
// Postgres. (ClickHouse holds the long-horizon copy; this covers the retained operational
// window and keeps the trend available even when ClickHouse is not deployed.)
func (s *Store) SNDSIPTrend(ctx context.Context, tenantID, ip string, days int) ([]SNDSTrendPoint, error) {
	if days <= 0 || days > 365 {
		days = 30
	}
	const q = `
SELECT data_date, filter_result, trap_hits, rcpt_count
FROM snds_ip_data
WHERE tenant_id = $1 AND ip = $2
ORDER BY data_date DESC
LIMIT $3`
	rows, err := s.Pool.Query(ctx, q, tenantID, ip, days)
	if err != nil {
		return nil, fmt.Errorf("snds ip trend: %w", err)
	}
	defer rows.Close()

	var out []SNDSTrendPoint
	for rows.Next() {
		var p SNDSTrendPoint
		if err := rows.Scan(&p.DataDate, &p.FilterResult, &p.TrapHits, &p.RcptCount); err != nil {
			return nil, fmt.Errorf("scan snds trend point: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Reverse to oldest-first for charting.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// JMRPComplaintInput records one parsed JMRP complaint; the store upserts it into the
// per-(domain, ip, day) summary, incrementing the count.
type JMRPComplaintInput struct {
	TenantID     string
	SenderDomain string
	SendingIP    string
	FeedbackType string
	Provider     string
	Day          time.Time // complaint_date
}

// UpsertJMRPComplaint increments the summary count for (tenant, sender_domain, sending_ip, day).
func (s *Store) UpsertJMRPComplaint(ctx context.Context, in JMRPComplaintInput) error {
	if in.FeedbackType == "" {
		in.FeedbackType = "abuse"
	}
	if in.Provider == "" {
		in.Provider = "microsoft"
	}
	const q = `
INSERT INTO jmrp_complaints
    (tenant_id, sender_domain, sending_ip, feedback_type, provider, complaint_date,
     complaint_count, first_seen, last_seen)
VALUES ($1, $2, $3, $4, $5, $6, 1, now(), now())
ON CONFLICT (tenant_id, sender_domain, sending_ip, complaint_date) DO UPDATE SET
    complaint_count = jmrp_complaints.complaint_count + 1,
    feedback_type   = EXCLUDED.feedback_type,
    last_seen       = now()`
	_, err := s.Pool.Exec(ctx, q,
		in.TenantID, in.SenderDomain, in.SendingIP, in.FeedbackType, in.Provider, in.Day)
	if err != nil {
		return fmt.Errorf("upsert jmrp complaint: %w", err)
	}
	return nil
}

// JMRPComplaintCount24h returns how many JMRP complaints a sending domain accrued in the last
// 24h under a tenant (sum of the summary counts). Drives the incident rollup.
func (s *Store) JMRPComplaintCount24h(ctx context.Context, tenantID, senderDomain string) (int, error) {
	const q = `
SELECT COALESCE(sum(complaint_count), 0)
FROM jmrp_complaints
WHERE tenant_id = $1 AND sender_domain = $2 AND last_seen > now() - interval '24 hours'`
	var n int
	if err := s.Pool.QueryRow(ctx, q, tenantID, senderDomain).Scan(&n); err != nil {
		return 0, fmt.Errorf("jmrp complaint count 24h: %w", err)
	}
	return n, nil
}

// JMRPComplaintRow is one row of the /v1/microsoft/jmrp complaint feed.
type JMRPComplaintRow struct {
	SenderDomain   string
	SendingIP      string
	FeedbackType   string
	Provider       string
	ComplaintDate  time.Time
	ComplaintCount int
	LastSeen       time.Time
}

// ListJMRPComplaints returns a tenant's JMRP complaint summary rows, most recent first.
// limit defaults to 100, capped 1000.
func (s *Store) ListJMRPComplaints(ctx context.Context, tenantID string, limit int) ([]JMRPComplaintRow, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	const q = `
SELECT sender_domain, sending_ip, feedback_type, provider,
       complaint_date, complaint_count, last_seen
FROM jmrp_complaints
WHERE tenant_id = $1
ORDER BY last_seen DESC
LIMIT $2`
	rows, err := s.Pool.Query(ctx, q, tenantID, limit)
	if err != nil {
		return nil, fmt.Errorf("list jmrp complaints: %w", err)
	}
	defer rows.Close()

	var out []JMRPComplaintRow
	for rows.Next() {
		var c JMRPComplaintRow
		if err := rows.Scan(
			&c.SenderDomain, &c.SendingIP, &c.FeedbackType, &c.Provider,
			&c.ComplaintDate, &c.ComplaintCount, &c.LastSeen,
		); err != nil {
			return nil, fmt.Errorf("scan jmrp complaint row: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
