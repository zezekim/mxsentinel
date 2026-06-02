package dmarc

import (
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"strings"
	"testing"

	chstore "github.com/zezekim/mxsentinel/internal/store/clickhouse"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
)

type fakeArch struct{ puts map[string][]byte }

func newFakeArch() *fakeArch { return &fakeArch{puts: map[string][]byte{}} }
func (f *fakeArch) Put(_ context.Context, key string, data []byte, _ string) (string, error) {
	f.puts[key] = data
	return key, nil
}

type fakeReports struct {
	tenant   string
	exists   bool
	inserted []pgstore.DMARCReportPointer
}

func (f *fakeReports) ResolveTenantByDomain(_ context.Context, _ string) (string, bool, error) {
	if f.tenant == "" {
		return "", false, nil
	}
	return f.tenant, true, nil
}
func (f *fakeReports) GetDomainID(_ context.Context, _, _ string) (string, bool, error) {
	return "dom-1", true, nil
}
func (f *fakeReports) DMARCReportExists(_ context.Context, _, _, _ string) (bool, error) {
	return f.exists, nil
}
func (f *fakeReports) InsertDMARCReport(_ context.Context, p pgstore.DMARCReportPointer) (string, error) {
	f.inserted = append(f.inserted, p)
	return "rep-1", nil
}

type fakeRecords struct{ rows []chstore.DMARCRecordRow }

func (f *fakeRecords) InsertDMARCRecords(_ context.Context, rows []chstore.DMARCRecordRow) error {
	f.rows = append(f.rows, rows...)
	return nil
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../../test/fixtures/dmarc/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

func TestIngestValid(t *testing.T) {
	arch, reports, records := newFakeArch(), &fakeReports{tenant: "tenant-1"}, &fakeRecords{}
	ing := NewIngestor(arch, reports, records)

	res, err := ing.IngestFile(context.Background(), "valid.xml", readFixture(t, "valid.xml"))
	if err != nil {
		t.Fatalf("IngestFile: %v", err)
	}
	if res.Status != StatusIngested {
		t.Fatalf("status = %q, want ingested (err=%v)", res.Status, res.Err)
	}
	if res.Records != 2 || len(records.rows) != 2 {
		t.Fatalf("records = %d / %d, want 2", res.Records, len(records.rows))
	}
	if len(reports.inserted) != 1 {
		t.Fatalf("pointer rows = %d, want 1", len(reports.inserted))
	}
	if _, ok := arch.puts[res.ObjectKey]; !ok {
		t.Errorf("raw not archived under %q", res.ObjectKey)
	}
	if !strings.HasPrefix(res.ObjectKey, "dmarc-raw/tenant-1/") {
		t.Errorf("unexpected object key: %q", res.ObjectKey)
	}
	for _, row := range records.rows {
		switch row.Disposition {
		case "none", "quarantine", "reject":
		default:
			t.Errorf("disposition not normalized: %q", row.Disposition)
		}
	}
}

func TestIngestDuplicate(t *testing.T) {
	arch, reports, records := newFakeArch(), &fakeReports{tenant: "tenant-1", exists: true}, &fakeRecords{}
	ing := NewIngestor(arch, reports, records)

	res, err := ing.IngestFile(context.Background(), "valid.xml", readFixture(t, "valid.xml"))
	if err != nil {
		t.Fatalf("IngestFile: %v", err)
	}
	if res.Status != StatusDuplicate {
		t.Fatalf("status = %q, want duplicate", res.Status)
	}
	if len(records.rows) != 0 || len(reports.inserted) != 0 {
		t.Error("duplicate should not write records or pointer")
	}
}

func TestIngestNoTenant(t *testing.T) {
	arch, reports, records := newFakeArch(), &fakeReports{tenant: ""}, &fakeRecords{}
	ing := NewIngestor(arch, reports, records)

	res, err := ing.IngestFile(context.Background(), "valid.xml", readFixture(t, "valid.xml"))
	if err != nil {
		t.Fatalf("IngestFile: %v", err)
	}
	if res.Status != StatusNoTenant {
		t.Fatalf("status = %q, want no_tenant", res.Status)
	}
}

func TestIngestMalformedQuarantined(t *testing.T) {
	arch, reports, records := newFakeArch(), &fakeReports{tenant: "tenant-1"}, &fakeRecords{}
	ing := NewIngestor(arch, reports, records)

	res, err := ing.IngestFile(context.Background(), "malformed.xml", readFixture(t, "malformed.xml"))
	if err != nil {
		t.Fatalf("malformed must not be fatal, got err: %v", err)
	}
	if res.Status != StatusQuarantined {
		t.Fatalf("status = %q, want quarantined", res.Status)
	}
	if res.Err == nil {
		t.Error("quarantined result should carry the cause")
	}
	if !strings.HasPrefix(res.ObjectKey, "dmarc-quarantine/") {
		t.Errorf("not quarantined to expected prefix: %q", res.ObjectKey)
	}
	if len(records.rows) != 0 || len(reports.inserted) != 0 {
		t.Error("malformed should not write records or pointer")
	}
}

func TestIngestGzip(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(readFixture(t, "valid.xml")); err != nil {
		t.Fatal(err)
	}
	zw.Close()

	arch, reports, records := newFakeArch(), &fakeReports{tenant: "tenant-1"}, &fakeRecords{}
	ing := NewIngestor(arch, reports, records)

	res, err := ing.IngestFile(context.Background(), "report.xml.gz", buf.Bytes())
	if err != nil {
		t.Fatalf("IngestFile gz: %v", err)
	}
	if res.Status != StatusIngested || res.Records != 2 {
		t.Fatalf("gz ingest: status=%q records=%d", res.Status, res.Records)
	}
	// The archived blob is the compressed bytes as received.
	if got := arch.puts[res.ObjectKey]; !bytes.Equal(got, buf.Bytes()) {
		t.Error("archived blob should be the raw (compressed) bytes")
	}
}
