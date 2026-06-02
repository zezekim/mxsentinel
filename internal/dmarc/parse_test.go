package dmarc

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"os"
	"testing"
)

const fixturesDir = "../../test/fixtures/dmarc/"

func TestParseValid(t *testing.T) {
	data, err := os.ReadFile(fixturesDir + "valid.xml")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	report, err := ParseBytes(data)
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}

	if report.OrgName != "google.com" {
		t.Errorf("OrgName: got %q, want %q", report.OrgName, "google.com")
	}
	if report.ReportID != "12345678901234567890" {
		t.Errorf("ReportID: got %q, want %q", report.ReportID, "12345678901234567890")
	}
	if report.Domain != "example.com" {
		t.Errorf("Domain: got %q, want %q", report.Domain, "example.com")
	}
	if report.Policy.P != "reject" {
		t.Errorf("Policy.P: got %q, want %q", report.Policy.P, "reject")
	}
	if len(report.Records) != 2 {
		t.Fatalf("len(Records): got %d, want 2", len(report.Records))
	}

	// Check first record
	r0 := report.Records[0]
	if r0.SourceIP != "198.51.100.5" {
		t.Errorf("Records[0].SourceIP: got %q, want %q", r0.SourceIP, "198.51.100.5")
	}
	if r0.Count != 42 {
		t.Errorf("Records[0].Count: got %d, want 42", r0.Count)
	}
	if !r0.DKIMAligned {
		t.Errorf("Records[0].DKIMAligned: got false, want true")
	}
	if !r0.SPFAligned {
		t.Errorf("Records[0].SPFAligned: got false, want true")
	}

	// Check second record has DKIMAligned/SPFAligned false
	r1 := report.Records[1]
	if r1.DKIMAligned {
		t.Errorf("Records[1].DKIMAligned: got true, want false")
	}
	if r1.SPFAligned {
		t.Errorf("Records[1].SPFAligned: got true, want false")
	}
}

func TestParseMalformed(t *testing.T) {
	data, err := os.ReadFile(fixturesDir + "malformed.xml")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	_, err = ParseBytes(data)
	if err == nil {
		t.Error("expected non-nil error for malformed XML, got nil")
	}
}

func TestDecompressGzip(t *testing.T) {
	original, err := os.ReadFile(fixturesDir + "valid.xml")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	// Compress in-memory
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write(original); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	decompressed, err := Decompress("report.xml.gz", buf.Bytes())
	if err != nil {
		t.Fatalf("Decompress: %v", err)
	}

	if !bytes.Equal(decompressed, original) {
		t.Error("decompressed bytes do not match original")
	}

	// Verify it re-parses correctly
	if _, err := ParseBytes(decompressed); err != nil {
		t.Fatalf("ParseBytes after decompress: %v", err)
	}
}

func TestDecompressZip(t *testing.T) {
	original, err := os.ReadFile(fixturesDir + "valid.xml")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	// Build zip in-memory
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	fw, err := zw.Create("report.xml")
	if err != nil {
		t.Fatalf("zip create entry: %v", err)
	}
	if _, err := fw.Write(original); err != nil {
		t.Fatalf("zip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}

	decompressed, err := Decompress("bundle.zip", buf.Bytes())
	if err != nil {
		t.Fatalf("Decompress: %v", err)
	}

	if !bytes.Equal(decompressed, original) {
		t.Error("decompressed zip bytes do not match original")
	}

	// Verify it re-parses correctly
	if _, err := ParseBytes(decompressed); err != nil {
		t.Fatalf("ParseBytes after zip decompress: %v", err)
	}
}

func TestDecompressPlain(t *testing.T) {
	original, err := os.ReadFile(fixturesDir + "valid.xml")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	out, err := Decompress("report.xml", original)
	if err != nil {
		t.Fatalf("Decompress: %v", err)
	}

	if !bytes.Equal(out, original) {
		t.Error("plain decompress should return data unchanged")
	}

	// Verify it re-parses correctly
	if _, err := ParseBytes(out); err != nil {
		t.Fatalf("ParseBytes plain: %v", err)
	}
}
