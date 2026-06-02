package clickhouse

import (
	"context"
	"fmt"
	"net"
	"time"
)

// DMARCRecordRow is one row for dmarc_records. dmarcparser builds these from
// parsed DMARC aggregate XML reports. Enum columns take their string label;
// the SourceIP column takes net.IP (IPv4-mapped IPv6 is handled automatically).
type DMARCRecordRow struct {
	ReportID     string
	OrgName      string
	TenantID     string // UUID string
	Domain       string
	DateBegin    time.Time
	DateEnd      time.Time
	SourceIP     net.IP
	Count        uint32
	Disposition  string // "none"|"quarantine"|"reject" (enum label)
	DKIMAligned  uint8  // 0/1
	SPFAligned   uint8  // 0/1
	HeaderFrom   string
	EnvelopeFrom string
	IngestedAt   time.Time
}

const dmarcInsertStmt = `INSERT INTO dmarc_records (report_id, org_name, tenant_id, domain, date_begin, date_end, source_ip, count, disposition, dkim_aligned, spf_aligned, header_from, envelope_from, ingested_at)`

// InsertDMARCRecords writes a batch of parsed DMARC rows.
func (s *Store) InsertDMARCRecords(ctx context.Context, rows []DMARCRecordRow) error {
	if len(rows) == 0 {
		return nil
	}
	batch, err := s.conn.PrepareBatch(ctx, dmarcInsertStmt)
	if err != nil {
		return fmt.Errorf("prepare dmarc_records batch: %w", err)
	}
	for i := range rows {
		r := &rows[i]
		if err := batch.Append(
			r.ReportID, r.OrgName, r.TenantID, r.Domain,
			r.DateBegin, r.DateEnd,
			ipOrZero(r.SourceIP),
			r.Count, r.Disposition,
			r.DKIMAligned, r.SPFAligned,
			r.HeaderFrom, r.EnvelopeFrom,
			r.IngestedAt,
		); err != nil {
			return fmt.Errorf("append row %d: %w", i, err)
		}
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("send dmarc_records batch: %w", err)
	}
	return nil
}
