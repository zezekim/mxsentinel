package snds

import "testing"

// A canonical SNDS automated-data CSV: header-less, one row per (IP, activity period). Columns:
// IP, ActivityStart, ActivityEnd, RCPT, DATA, Recipients, FilterResult, ComplaintRate,
// TrapPeriod, TrapHits, SampleHELO, SampleMAILFROM.
const sampleSNDS = "" +
	"203.0.113.10,12/31/2025 12:30 AM,12/31/2025 1:00 AM,1000,900,1000,GREEN,< 0.1%,,0,mail.client.example,bounce@client.example\r\n" +
	"203.0.113.11,12/31/2025 1:00 AM,12/31/2025 1:30 AM,5000,4800,5000,RED,1% - < 2%,12/31/2025,7,relay.client.example,news@shop.example\r\n" +
	"198.51.100.5,1/1/2026 2:00 PM,1/1/2026 2:30 PM,200,180,200,YELLOW,0.1% - < 1%,,0,,\r\n"

func TestParseSNDS(t *testing.T) {
	rows, err := ParseSNDS([]byte(sampleSNDS))
	if err != nil {
		t.Fatalf("ParseSNDS: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}

	green := rows[0]
	if green.IP != "203.0.113.10" || green.FilterResult != "GREEN" {
		t.Errorf("row0 = %+v, want GREEN 203.0.113.10", green)
	}
	if green.RcptCount != 1000 || green.DataCount != 900 || green.MsgRecipients != 1000 {
		t.Errorf("row0 counts = %d/%d/%d, want 1000/900/1000", green.RcptCount, green.DataCount, green.MsgRecipients)
	}
	if green.DataDate.Format("2006-01-02") != "2025-12-31" {
		t.Errorf("row0 data_date = %s, want 2025-12-31", green.DataDate.Format("2006-01-02"))
	}
	if green.SampleHELO != "mail.client.example" || green.SampleFrom != "bounce@client.example" {
		t.Errorf("row0 samples = %q/%q", green.SampleHELO, green.SampleFrom)
	}

	red := rows[1]
	if red.FilterResult != "RED" || red.TrapHits != 7 {
		t.Errorf("row1 = %+v, want RED trap=7", red)
	}
	if red.ComplaintBand != "1% - < 2%" {
		t.Errorf("row1 complaint band = %q", red.ComplaintBand)
	}
	if !IsBadFilterResult(red.FilterResult) {
		t.Errorf("row1 should be a bad filter result")
	}

	yellow := rows[2]
	if yellow.FilterResult != "YELLOW" || yellow.DataDate.Format("2006-01-02") != "2026-01-01" {
		t.Errorf("row2 = %+v, want YELLOW 2026-01-01", yellow)
	}
}

func TestParseSNDS_SkipsGarbage(t *testing.T) {
	const raw = "IP,Start,End,RCPT\r\n" + // header-ish line: non-IP first col -> skipped
		"not-an-ip,foo,bar,1,2,3,GREEN\r\n" + // bad IP -> skipped
		"\r\n" + // blank -> skipped
		"203.0.113.99,1/2/2026 3:04 PM,1/2/2026 3:34 PM,10,9,10,GREEN,< 0.1%,,0,helo.example,from@example\r\n"
	rows, err := ParseSNDS([]byte(raw))
	if err != nil {
		t.Fatalf("ParseSNDS: %v", err)
	}
	if len(rows) != 1 || rows[0].IP != "203.0.113.99" {
		t.Fatalf("got %+v, want single 203.0.113.99 row", rows)
	}
}

func TestNormalizeFilterResult(t *testing.T) {
	cases := map[string]string{
		"green": "GREEN", "YELLOW": "YELLOW", "Red": "RED",
		"": "", "unknown": "", "  RED  ": "RED",
	}
	for in, want := range cases {
		if got := NormalizeFilterResult(in); got != want {
			t.Errorf("NormalizeFilterResult(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsBadFilterResult(t *testing.T) {
	for _, s := range []string{"RED", "red", " red "} {
		if !IsBadFilterResult(s) {
			t.Errorf("IsBadFilterResult(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"GREEN", "YELLOW", ""} {
		if IsBadFilterResult(s) {
			t.Errorf("IsBadFilterResult(%q) = true, want false", s)
		}
	}
}

func TestParseSNDSTime(t *testing.T) {
	for _, s := range []string{"12/31/2025 12:30 AM", "1/2/2026", "2026-01-02 15:04:05"} {
		if parseSNDSTime(s).IsZero() {
			t.Errorf("parseSNDSTime(%q) returned zero", s)
		}
	}
	if !parseSNDSTime("nonsense").IsZero() {
		t.Errorf("parseSNDSTime(nonsense) should be zero")
	}
}
