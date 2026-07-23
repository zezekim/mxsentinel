package tlsrpt

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"fmt"
	"io"
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
	TLSRPTReportExists(ctx context.Context, tenantID, reportID string) (bool, error)
	InsertTLSRPTReport(ctx context.Context, p pgstore.TLSRPTReportPointer) (string, error)
}

// RecordStore writes parsed detail rows (implemented by internal/store/clickhouse.Store).
type RecordStore interface {
	InsertTLSRPTResults(ctx context.Context, rows []chstore.TLSRPTResultRow) error
}

// Ingestor turns raw TLS-RPT files into archived blobs + a pointer row + ClickHouse rows.
type Ingestor struct {
	arch    Archiver
	reports ReportStore
	records RecordStore
	now     func() time.Time
}

// NewIngestor wires the ingestor. now defaults to time.Now.
func NewIngestor(arch Archiver, reports ReportStore, records RecordStore) *Ingestor {
	return &Ingestor{arch: arch, reports: reports, records: records, now: time.Now}
}

// IngestFile processes one report file (optionally gzip compressed). Expected outcomes
// (duplicate, no-tenant, quarantined) return a Result with a nil error; only genuine
// infrastructure failures return an error. Malformed input is quarantined, never fatal.
func (i *Ingestor) IngestFile(ctx context.Context, filename string, data []byte) (Result, error) {
	jsonData, err := decompress(filename, data)
	if err != nil {
		return i.quarantine(ctx, filename, data, fmt.Errorf("decompress: %w", err)), nil
	}
	rep, err := ParseBytes(jsonData)
	if err != nil {
		return i.quarantine(ctx, filename, data, fmt.Errorf("parse: %w", err)), nil
	}
	domain := rep.PrimaryDomain()
	if domain == "" {
		return i.quarantine(ctx, filename, data, fmt.Errorf("report has no policy-domain")), nil
	}

	tenantID, ok, err := i.reports.ResolveTenantByDomain(ctx, domain)
	if err != nil {
		return Result{}, fmt.Errorf("resolve tenant: %w", err)
	}
	if !ok {
		return Result{Status: StatusNoTenant, ReportID: rep.ReportID, Domain: domain}, nil
	}

	exists, err := i.reports.TLSRPTReportExists(ctx, tenantID, rep.ReportID)
	if err != nil {
		return Result{}, fmt.Errorf("dedupe check: %w", err)
	}
	if exists {
		return Result{Status: StatusDuplicate, ReportID: rep.ReportID, Domain: domain, TenantID: tenantID}, nil
	}

	key := rawKey(tenantID, rep.DateBegin, rep.ReportID)
	if _, err := i.arch.Put(ctx, key, data, contentType(filename)); err != nil {
		return Result{}, fmt.Errorf("archive raw: %w", err)
	}

	domainID, _, _ := i.reports.GetDomainID(ctx, tenantID, domain) // best-effort; may be ""
	success, failure := rep.Totals()
	if _, err := i.reports.InsertTLSRPTReport(ctx, pgstore.TLSRPTReportPointer{
		TenantID:     tenantID,
		DomainID:     domainID,
		DomainName:   domain,
		OrgName:      rep.OrganizationName,
		ReportID:     rep.ReportID,
		DateBegin:    rep.DateBegin,
		DateEnd:      rep.DateEnd,
		ObjectKey:    key,
		PolicyCount:  len(rep.Policies),
		SuccessCount: success,
		FailureCount: failure,
	}); err != nil {
		return Result{}, fmt.Errorf("insert pointer: %w", err)
	}

	rows := i.mapRows(tenantID, rep)
	if err := i.records.InsertTLSRPTResults(ctx, rows); err != nil {
		return Result{}, fmt.Errorf("insert records: %w", err)
	}

	return Result{
		Status: StatusIngested, ReportID: rep.ReportID, Domain: domain,
		TenantID: tenantID, ObjectKey: key, Records: len(rows),
	}, nil
}

// mapRows emits one ClickHouse row per policy summary (result_type "successful") plus one
// row per failure detail, so both aggregate success and per-MTA failures are queryable.
func (i *Ingestor) mapRows(tenantID string, rep *Report) []chstore.TLSRPTResultRow {
	now := i.now()
	var rows []chstore.TLSRPTResultRow
	for _, p := range rep.Policies {
		rows = append(rows, chstore.TLSRPTResultRow{
			ReportID:     rep.ReportID,
			OrgName:      rep.OrganizationName,
			TenantID:     tenantID,
			PolicyDomain: p.PolicyDomain,
			PolicyType:   p.PolicyType,
			DateBegin:    rep.DateBegin,
			DateEnd:      rep.DateEnd,
			ResultType:   "successful",
			SuccessCount: p.SuccessCount,
			FailureCount: 0,
			IngestedAt:   now,
		})
		for _, f := range p.FailureDetails {
			rows = append(rows, chstore.TLSRPTResultRow{
				ReportID:            rep.ReportID,
				OrgName:             rep.OrganizationName,
				TenantID:            tenantID,
				PolicyDomain:        p.PolicyDomain,
				PolicyType:          p.PolicyType,
				DateBegin:           rep.DateBegin,
				DateEnd:             rep.DateEnd,
				ResultType:          orUnknown(f.ResultType),
				SendingMTAIP:        net.ParseIP(strings.TrimSpace(f.SendingMTAIP)),
				ReceivingMXHostname: f.ReceivingMXHostname,
				ReceivingIP:         net.ParseIP(strings.TrimSpace(f.ReceivingIP)),
				SuccessCount:        0,
				FailureCount:        f.FailedSessionCount,
				IngestedAt:          now,
			})
		}
	}
	return rows
}

func (i *Ingestor) quarantine(ctx context.Context, filename string, data []byte, cause error) Result {
	key := quarantineKey(filename)
	_, _ = i.arch.Put(ctx, key, data, "application/octet-stream") // best-effort
	return Result{Status: StatusQuarantined, ObjectKey: key, Err: cause}
}

// decompress gunzips data when the filename ends in .gz; otherwise returns it unchanged.
func decompress(filename string, data []byte) ([]byte, error) {
	if !strings.HasSuffix(strings.ToLower(filename), ".gz") {
		return data, nil
	}
	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("tlsrpt: gzip open: %w", err)
	}
	defer gr.Close()
	out, err := io.ReadAll(gr)
	if err != nil {
		return nil, fmt.Errorf("tlsrpt: gzip read: %w", err)
	}
	return out, nil
}

// rawKey: tlsrpt-raw/<tenant>/<yyyy>/<mm>/<report-id>.json.gz
func rawKey(tenantID string, when time.Time, reportID string) string {
	w := when.UTC()
	if w.IsZero() {
		w = time.Now().UTC()
	}
	return path.Join("tlsrpt-raw", tenantID,
		fmt.Sprintf("%04d", w.Year()), fmt.Sprintf("%02d", int(w.Month())),
		sanitize(reportID)+".json.gz")
}

func quarantineKey(filename string) string {
	base := path.Base(filename)
	if base == "" || base == "." || base == "/" {
		base = "report"
	}
	h := sha1.Sum([]byte(filename))
	return path.Join("tlsrpt-quarantine", fmt.Sprintf("%x-%s", h[:4], sanitize(base)))
}

func contentType(filename string) string {
	if strings.HasSuffix(strings.ToLower(filename), ".gz") {
		return "application/gzip"
	}
	return "application/tlsrpt+json"
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unknown"
	}
	return s
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
