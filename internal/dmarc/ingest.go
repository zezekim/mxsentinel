package dmarc

import (
	"context"
	"crypto/sha1"
	"fmt"
	"net"
	"path"
	"strings"
	"time"

	chstore "github.com/zezekim/mxsentinel/internal/store/clickhouse"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
)

// Status is the outcome of ingesting one report file.
type Status string

const (
	StatusIngested    Status = "ingested"
	StatusDuplicate   Status = "duplicate"
	StatusNoTenant    Status = "no_tenant"
	StatusQuarantined Status = "quarantined"
)

// Result describes what happened to one ingested file.
type Result struct {
	Status    Status
	ReportID  string
	Domain    string
	TenantID  string
	ObjectKey string
	Records   int
	Err       error // reason, when quarantined
}

// Archiver stores raw report bytes (implemented by internal/store/objectstore.Store).
type Archiver interface {
	Put(ctx context.Context, key string, data []byte, contentType string) (string, error)
}

// ReportStore records report pointers and resolves tenants/domains (implemented by
// internal/store/postgres.Store).
type ReportStore interface {
	ResolveTenantByDomain(ctx context.Context, name string) (string, bool, error)
	GetDomainID(ctx context.Context, tenantID, name string) (string, bool, error)
	DMARCReportExists(ctx context.Context, tenantID, orgName, reportID string) (bool, error)
	InsertDMARCReport(ctx context.Context, p pgstore.DMARCReportPointer) (string, error)
}

// RecordStore writes parsed records (implemented by internal/store/clickhouse.Store).
type RecordStore interface {
	InsertDMARCRecords(ctx context.Context, rows []chstore.DMARCRecordRow) error
}

// Ingestor turns raw report files into archived blobs + a pointer row + ClickHouse rows.
type Ingestor struct {
	arch    Archiver
	reports ReportStore
	records RecordStore
	now     func() time.Time
}

// NewIngestor wires the ingestor. now defaults to time.Now when nil.
func NewIngestor(arch Archiver, reports ReportStore, records RecordStore) *Ingestor {
	return &Ingestor{arch: arch, reports: reports, records: records, now: time.Now}
}

// IngestFile processes one report file (optionally gz/zip compressed). Expected outcomes
// (duplicate, no-tenant, quarantined) return a Result with a nil error; only genuine
// infrastructure failures return an error. Malformed input is quarantined, never fatal —
// the daemon keeps running.
func (i *Ingestor) IngestFile(ctx context.Context, filename string, data []byte) (Result, error) {
	xmlData, err := Decompress(filename, data)
	if err != nil {
		return i.quarantine(ctx, filename, data, fmt.Errorf("decompress: %w", err)), nil
	}
	rep, err := ParseBytes(xmlData)
	if err != nil {
		return i.quarantine(ctx, filename, data, fmt.Errorf("parse: %w", err)), nil
	}
	if rep.ReportID == "" || rep.Domain == "" {
		return i.quarantine(ctx, filename, data, fmt.Errorf("report missing report_id or domain")), nil
	}

	tenantID, ok, err := i.reports.ResolveTenantByDomain(ctx, rep.Domain)
	if err != nil {
		return Result{}, fmt.Errorf("resolve tenant: %w", err)
	}
	if !ok {
		return Result{Status: StatusNoTenant, ReportID: rep.ReportID, Domain: rep.Domain}, nil
	}

	exists, err := i.reports.DMARCReportExists(ctx, tenantID, rep.OrgName, rep.ReportID)
	if err != nil {
		return Result{}, fmt.Errorf("dedupe check: %w", err)
	}
	if exists {
		return Result{Status: StatusDuplicate, ReportID: rep.ReportID, Domain: rep.Domain, TenantID: tenantID}, nil
	}

	// Archive the report exactly as received (compressed if it arrived compressed).
	key := rawKey(tenantID, rep.DateBegin, rep.ReportID)
	if _, err := i.arch.Put(ctx, key, data, contentType(filename)); err != nil {
		return Result{}, fmt.Errorf("archive raw: %w", err)
	}

	domainID, _, _ := i.reports.GetDomainID(ctx, tenantID, rep.Domain) // best-effort; may be ""
	if _, err := i.reports.InsertDMARCReport(ctx, pgstore.DMARCReportPointer{
		TenantID:    tenantID,
		DomainID:    domainID,
		DomainName:  rep.Domain,
		OrgName:     rep.OrgName,
		ReportID:    rep.ReportID,
		DateBegin:   rep.DateBegin,
		DateEnd:     rep.DateEnd,
		ObjectKey:   key,
		RecordCount: len(rep.Records),
	}); err != nil {
		return Result{}, fmt.Errorf("insert pointer: %w", err)
	}

	rows := i.mapRows(tenantID, rep)
	if err := i.records.InsertDMARCRecords(ctx, rows); err != nil {
		return Result{}, fmt.Errorf("insert records: %w", err)
	}

	return Result{
		Status: StatusIngested, ReportID: rep.ReportID, Domain: rep.Domain,
		TenantID: tenantID, ObjectKey: key, Records: len(rows),
	}, nil
}

func (i *Ingestor) mapRows(tenantID string, rep *Report) []chstore.DMARCRecordRow {
	now := i.now()
	rows := make([]chstore.DMARCRecordRow, 0, len(rep.Records))
	for _, r := range rep.Records {
		rows = append(rows, chstore.DMARCRecordRow{
			ReportID:     rep.ReportID,
			OrgName:      rep.OrgName,
			TenantID:     tenantID,
			Domain:       rep.Domain,
			DateBegin:    rep.DateBegin,
			DateEnd:      rep.DateEnd,
			SourceIP:     net.ParseIP(strings.TrimSpace(r.SourceIP)),
			Count:        uint32(max(0, r.Count)),
			Disposition:  normalizeDisposition(r.Disposition),
			DKIMAligned:  b2u8(r.DKIMAligned),
			SPFAligned:   b2u8(r.SPFAligned),
			HeaderFrom:   r.HeaderFrom,
			EnvelopeFrom: r.EnvelopeFrom,
			IngestedAt:   now,
		})
	}
	return rows
}

func (i *Ingestor) quarantine(ctx context.Context, filename string, data []byte, cause error) Result {
	key := quarantineKey(filename)
	_, _ = i.arch.Put(ctx, key, data, "application/octet-stream") // best-effort
	return Result{Status: StatusQuarantined, ObjectKey: key, Err: cause}
}

// rawKey mirrors objectstore.DMARCRawKey: dmarc-raw/<tenant>/<yyyy>/<mm>/<report-id>.xml.gz
func rawKey(tenantID string, when time.Time, reportID string) string {
	w := when.UTC()
	return path.Join("dmarc-raw", tenantID,
		fmt.Sprintf("%04d", w.Year()), fmt.Sprintf("%02d", int(w.Month())),
		sanitize(reportID)+".xml.gz")
}

func quarantineKey(filename string) string {
	base := path.Base(filename)
	if base == "" || base == "." || base == "/" {
		base = "report"
	}
	// disambiguate by content-independent name hash to avoid collisions
	h := sha1.Sum([]byte(filename))
	return path.Join("dmarc-quarantine", fmt.Sprintf("%x-%s", h[:4], sanitize(base)))
}

func contentType(filename string) string {
	switch {
	case strings.HasSuffix(filename, ".gz"):
		return "application/gzip"
	case strings.HasSuffix(filename, ".zip"):
		return "application/zip"
	default:
		return "application/xml"
	}
}

func normalizeDisposition(d string) string {
	switch strings.ToLower(strings.TrimSpace(d)) {
	case "quarantine":
		return "quarantine"
	case "reject":
		return "reject"
	default:
		return "none"
	}
}

func b2u8(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}

func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		default:
			return '_'
		}
	}, s)
}
