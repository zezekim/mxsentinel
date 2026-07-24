package report

import (
	"strings"
	"testing"
	"time"
)

func TestCommas(t *testing.T) {
	cases := map[uint64]string{0: "0", 5: "5", 42: "42", 999: "999", 1000: "1,000", 1234567: "1,234,567", 12345: "12,345"}
	for in, want := range cases {
		if got := commas(in); got != want {
			t.Errorf("commas(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestText_FullReport(t *testing.T) {
	r := DomainReport{
		Domain:      "pchslive.com",
		PeriodStart: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		PeriodEnd:   time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC),
		Core:        Counts{Delivered: 1234, Bounced: 56, Deferred: 5, Rejected: 2, Total: 1297},
		Providers: []ProviderRow{
			{Provider: "microsoft", Counts: Counts{Delivered: 780, Bounced: 60, Total: 842}},
			{Provider: "google", Counts: Counts{Delivered: 398, Bounced: 2, Total: 401}},
		},
		Score:     &ScoreInfo{Score: 82, Grade: "B", Coverage: 0.76},
		Placement: []PlacementRow{{Provider: "google", Inbox: 18, Total: 20}},
	}
	out := r.Text()

	for _, want := range []string{
		"pchslive.com · 2026-07-01 to 2026-07-31",
		"Health score: 82 (B) · coverage 76%",
		"Sent 1,297",
		"Delivered 1,234 (95.1%)",
		"Microsoft", "Google",
		"Inbox placement (seed tests, relay-wide)",
		"inbox 90%  (18/20)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report text missing %q\n---\n%s", want, out)
		}
	}
}

func TestText_NoVolume(t *testing.T) {
	r := DomainReport{Domain: "quiet.example", PeriodStart: time.Now().Add(-24 * time.Hour), PeriodEnd: time.Now()}
	out := r.Text()
	if !strings.Contains(out, "No mail sent from this domain") {
		t.Fatalf("expected no-volume note, got:\n%s", out)
	}
	if strings.Contains(out, "By provider") {
		t.Fatalf("should not render provider section with no volume")
	}
}
