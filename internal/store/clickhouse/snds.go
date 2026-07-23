package clickhouse

import (
	"context"
	"fmt"
	"time"
)

// SNDSRow is one per-IP-per-day SNDS record streamed to the long-horizon ClickHouse history
// table (migrations/clickhouse/00006_snds.sql). It is the analytics copy of the authoritative
// Postgres snds_ip_data row; the enum column takes the string label ("", GREEN, YELLOW, RED).
type SNDSRow struct {
	TenantID      string
	IP            string
	DataDate      time.Time // truncated to a day
	RcptCount     uint64
	DataCount     uint64
	MsgRecipients uint64
	FilterResult  string
	ComplaintBand string
	TrapHits      uint32
	SampleHELO    string
	SampleFrom    string
	FetchedAt     time.Time
}

const sndsInsertStmt = `INSERT INTO snds_ip_data (
	tenant_id, ip, data_date, rcpt_count, data_count, message_recipients,
	filter_result, complaint_band, trap_hits, sample_helo, sample_from, fetched_at
)`

// InsertSNDSRows writes a batch of SNDS per-IP-per-day rows. ReplacingMergeTree dedupes
// re-polled days on (tenant_id, ip, data_date). No-op on an empty batch.
func (s *Store) InsertSNDSRows(ctx context.Context, rows []SNDSRow) error {
	if len(rows) == 0 {
		return nil
	}
	batch, err := s.conn.PrepareBatch(ctx, sndsInsertStmt)
	if err != nil {
		return fmt.Errorf("prepare snds_ip_data batch: %w", err)
	}
	for i := range rows {
		r := &rows[i]
		if err := batch.Append(
			r.TenantID, r.IP, r.DataDate, r.RcptCount, r.DataCount, r.MsgRecipients,
			r.FilterResult, r.ComplaintBand, r.TrapHits, r.SampleHELO, r.SampleFrom, r.FetchedAt,
		); err != nil {
			return fmt.Errorf("append snds row %d: %w", i, err)
		}
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("send snds_ip_data batch: %w", err)
	}
	return nil
}

// SNDSTrendPoint is one day of a single IP's SNDS history for a trend chart.
type SNDSTrendPoint struct {
	DataDate     time.Time
	FilterResult string
	TrapHits     uint32
	RcptCount    uint64
}

// SNDSIPTrend returns up to `days` most-recent daily rows for one IP under a tenant,
// oldest-first, deduped via FINAL. This is the long-horizon trend read.
func (s *Store) SNDSIPTrend(ctx context.Context, tenantID, ip string, days int) ([]SNDSTrendPoint, error) {
	if days <= 0 || days > 365 {
		days = 30
	}
	const q = `
SELECT data_date, filter_result, trap_hits, rcpt_count
FROM snds_ip_data FINAL
WHERE tenant_id = ? AND ip = ?
ORDER BY data_date DESC
LIMIT ?`
	rows, err := s.conn.Query(ctx, q, tenantID, ip, uint64(days))
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
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}
