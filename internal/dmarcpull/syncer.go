package dmarcpull

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"time"

	chstore "github.com/zezekim/mxsentinel/internal/store/clickhouse"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
)

const (
	pullObjectKeyPrefix = "pull://dmarc.squidix.org/"
	pageLimit           = 100
)

// Syncer pulls new DMARC reports from the remote API and writes them into
// Postgres + ClickHouse using the same stores as dmarcd.
type Syncer struct {
	client       *Client
	pg           *pgstore.Store
	ch           *chstore.Store
	tenantID     string
	lookbackDays int
	log          *slog.Logger
}

// NewSyncer constructs a Syncer.
func NewSyncer(client *Client, pg *pgstore.Store, ch *chstore.Store, tenantID string, lookbackDays int, log *slog.Logger) *Syncer {
	if lookbackDays <= 0 {
		lookbackDays = 30
	}
	return &Syncer{
		client:       client,
		pg:           pg,
		ch:           ch,
		tenantID:     tenantID,
		lookbackDays: lookbackDays,
		log:          log,
	}
}

// SyncResult summarises one sync run.
type SyncResult struct {
	Fetched    int
	Inserted   int
	Skipped    int // duplicates
	Records    int // ClickHouse rows inserted
	MaxDateEnd time.Time
}

// Run performs one full sync: pages through all reports since the cursor,
// fetches records for each new report, and updates the cursor on success.
func (s *Syncer) Run(ctx context.Context) error {
	cursor, err := s.pg.GetDmarcPullCursor(ctx, s.tenantID)
	if err != nil {
		return fmt.Errorf("get cursor: %w", err)
	}

	// On first run, fall back to now - lookback.
	since := cursor
	if since.IsZero() {
		since = time.Now().UTC().AddDate(0, 0, -s.lookbackDays)
		s.log.Info("dmarc pull: first run", "since", since.Format(time.RFC3339))
	}

	res, err := s.syncAll(ctx, since)
	if err != nil {
		return err
	}

	s.log.Info("dmarc pull: sync done",
		"fetched", res.Fetched,
		"inserted", res.Inserted,
		"skipped", res.Skipped,
		"records", res.Records,
	)

	if !res.MaxDateEnd.IsZero() && res.MaxDateEnd.After(cursor) {
		if err := s.pg.SetDmarcPullCursor(ctx, s.tenantID, res.MaxDateEnd); err != nil {
			return fmt.Errorf("set cursor: %w", err)
		}
	}
	return nil
}

func (s *Syncer) syncAll(ctx context.Context, since time.Time) (SyncResult, error) {
	var res SyncResult
	pageCursor := ""

	for {
		page, err := s.client.ListReports(ctx, since, pageCursor, pageLimit)
		if err != nil {
			return res, fmt.Errorf("list reports: %w", err)
		}

		for _, rpt := range page.Reports {
			res.Fetched++
			n, err := s.syncReport(ctx, rpt)
			if err != nil {
				s.log.Warn("dmarc pull: skip report", "report_id", rpt.ReportID, "domain", rpt.Domain, "err", err)
				continue
			}
			if n < 0 {
				res.Skipped++
			} else {
				res.Inserted++
				res.Records += n
			}
			if rpt.DateEnd.After(res.MaxDateEnd) {
				res.MaxDateEnd = rpt.DateEnd
			}
		}

		if page.NextCursor == "" {
			break
		}
		pageCursor = page.NextCursor
	}
	return res, nil
}

// syncReport inserts one report + its records. Returns -1 if already exists,
// or the number of ClickHouse records inserted on success.
func (s *Syncer) syncReport(ctx context.Context, rpt RemoteReport) (int, error) {
	exists, err := s.pg.DMARCReportExists(ctx, s.tenantID, rpt.OrgName, rpt.ReportID)
	if err != nil {
		return 0, err
	}
	if exists {
		return -1, nil
	}

	// Fetch per-source records.
	detail, err := s.client.GetRecords(ctx, rpt.ID)
	if err != nil {
		return 0, err
	}

	// Resolve domain_id (best-effort — may be empty if domain not registered).
	domainID := ""
	if rpt.Domain != "" {
		id, _, _ := s.pg.GetDomainID(ctx, s.tenantID, rpt.Domain)
		domainID = id
	}

	// Insert Postgres pointer. Use a synthetic object key since we don't archive raw XML.
	_, err = s.pg.InsertDMARCReport(ctx, pgstore.DMARCReportPointer{
		TenantID:    s.tenantID,
		DomainID:    domainID,
		DomainName:  rpt.Domain,
		OrgName:     rpt.OrgName,
		ReportID:    rpt.ReportID,
		DateBegin:   rpt.DateBegin,
		DateEnd:     rpt.DateEnd,
		ObjectKey:   pullObjectKeyPrefix + rpt.ID,
		RecordCount: rpt.RecordCount,
	})
	if err != nil {
		return 0, fmt.Errorf("insert report pointer: %w", err)
	}

	// Build and insert ClickHouse rows.
	rows := make([]chstore.DMARCRecordRow, 0, len(detail.Records))
	now := time.Now().UTC()
	domain := rpt.Domain
	if detail.Domain != "" {
		domain = detail.Domain
	}
	for _, rec := range detail.Records {
		ip := net.ParseIP(rec.SourceIP)
		if ip == nil {
			ip = net.IPv4zero
		}
		var dkimAligned, spfAligned uint8
		if rec.DKIMAligned {
			dkimAligned = 1
		}
		if rec.SPFAligned {
			spfAligned = 1
		}
		rows = append(rows, chstore.DMARCRecordRow{
			ReportID:     rpt.ReportID,
			OrgName:      rpt.OrgName,
			TenantID:     s.tenantID,
			Domain:       domain,
			DateBegin:    rpt.DateBegin,
			DateEnd:      rpt.DateEnd,
			SourceIP:     ip,
			Count:        rec.Count,
			Disposition:  rec.Disposition,
			DKIMAligned:  dkimAligned,
			SPFAligned:   spfAligned,
			HeaderFrom:   rec.HeaderFrom,
			EnvelopeFrom: rec.EnvelopeFrom,
			IngestedAt:   now,
		})
	}

	if err := s.ch.InsertDMARCRecords(ctx, rows); err != nil {
		return 0, fmt.Errorf("insert clickhouse records: %w", err)
	}
	return len(rows), nil
}
