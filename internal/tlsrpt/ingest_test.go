package tlsrpt

import (
	"context"
	"testing"

	chstore "github.com/zezekim/mxsentinel/internal/store/clickhouse"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
)

type fakeArch struct{ puts int }

func (f *fakeArch) Put(_ context.Context, key string, _ []byte, _ string) (string, error) {
	f.puts++
	return key, nil
}

type fakeReports struct {
	tenant  string
	exists  bool
	pointer pgstore.TLSRPTReportPointer
	inserts int
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
func (f *fakeReports) TLSRPTReportExists(_ context.Context, _, _ string) (bool, error) {
	return f.exists, nil
}
func (f *fakeReports) InsertTLSRPTReport(_ context.Context, p pgstore.TLSRPTReportPointer) (string, error) {
	f.pointer = p
	f.inserts++
	return "rep-1", nil
}

type fakeRecords struct{ rows []chstore.TLSRPTResultRow }

func (f *fakeRecords) InsertTLSRPTResults(_ context.Context, rows []chstore.TLSRPTResultRow) error {
	f.rows = append(f.rows, rows...)
	return nil
}

func TestIngestFile(t *testing.T) {
	t.Run("ingested", func(t *testing.T) {
		arch, reports, records := &fakeArch{}, &fakeReports{tenant: "t-1"}, &fakeRecords{}
		ing := NewIngestor(arch, reports, records)
		res, err := ing.IngestFile(context.Background(), "report.json", []byte(sampleReport))
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != StatusIngested {
			t.Fatalf("status = %q (%v)", res.Status, res.Err)
		}
		if reports.inserts != 1 {
			t.Errorf("pointer inserts = %d", reports.inserts)
		}
		if reports.pointer.SuccessCount != 5326 || reports.pointer.FailureCount != 303 {
			t.Errorf("pointer totals = %+v", reports.pointer)
		}
		// 1 summary row + 2 failure rows.
		if len(records.rows) != 3 {
			t.Errorf("rows = %d, want 3", len(records.rows))
		}
	})

	t.Run("no tenant", func(t *testing.T) {
		ing := NewIngestor(&fakeArch{}, &fakeReports{tenant: ""}, &fakeRecords{})
		res, err := ing.IngestFile(context.Background(), "report.json", []byte(sampleReport))
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != StatusNoTenant {
			t.Errorf("status = %q", res.Status)
		}
	})

	t.Run("duplicate", func(t *testing.T) {
		ing := NewIngestor(&fakeArch{}, &fakeReports{tenant: "t-1", exists: true}, &fakeRecords{})
		res, err := ing.IngestFile(context.Background(), "report.json", []byte(sampleReport))
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != StatusDuplicate {
			t.Errorf("status = %q", res.Status)
		}
	})

	t.Run("malformed quarantined", func(t *testing.T) {
		arch := &fakeArch{}
		ing := NewIngestor(arch, &fakeReports{tenant: "t-1"}, &fakeRecords{})
		res, err := ing.IngestFile(context.Background(), "bad.json", []byte("{not json"))
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != StatusQuarantined || res.Err == nil {
			t.Errorf("status = %q err = %v", res.Status, res.Err)
		}
		if arch.puts == 0 {
			t.Errorf("expected quarantine archive write")
		}
	})
}
