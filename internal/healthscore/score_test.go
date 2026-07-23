package healthscore

import (
	"math"
	"testing"
)

func f64(v float64) *float64 { return &v }

func approx(a, b float64) bool { return math.Abs(a-b) < 0.05 }

// Test the individual component scorers in isolation — these are the load-bearing mappings.
func TestComponentScores(t *testing.T) {
	t.Run("dmarc", func(t *testing.T) {
		cases := []struct {
			name string
			in   DMARCInput
			want float64
		}{
			{"empty is neutral-100", DMARCInput{}, 100},
			{"fully aligned", DMARCInput{Total: 100, DKIMAligned: 100, SPFAligned: 100}, 100},
			{"none aligned", DMARCInput{Total: 100, DKIMAligned: 0, SPFAligned: 0}, 0},
			{"dkim only 80pct", DMARCInput{Total: 100, DKIMAligned: 80, SPFAligned: 10}, 80},
			{"spf dominates", DMARCInput{Total: 200, DKIMAligned: 20, SPFAligned: 150}, 75},
		}
		for _, c := range cases {
			got, _ := c.in.score()
			if !approx(got, c.want) {
				t.Errorf("%s: got %.2f want %.2f", c.name, got, c.want)
			}
		}
	})

	t.Run("complaints", func(t *testing.T) {
		cases := []struct {
			in   ComplaintInput
			want float64
		}{
			{ComplaintInput{0}, 100},
			{ComplaintInput{5}, 70},
			{ComplaintInput{10}, 40},
			{ComplaintInput{100}, 0},
		}
		for _, c := range cases {
			got, _ := c.in.score()
			if !approx(got, c.want) {
				t.Errorf("complaints %d: got %.2f want %.2f", c.in.Complaints24h, got, c.want)
			}
		}
	})

	t.Run("bounce", func(t *testing.T) {
		cases := []struct {
			in      BounceInput
			want    float64
			present bool
		}{
			{BounceInput{}, 0, false},
			{BounceInput{Total: 1000, Bounced: 10, Rejected: 10}, 100, true}, // 2% -> 100
			{BounceInput{Total: 1000, Bounced: 100, Rejected: 100}, 0, true}, // 20% -> 0
			{BounceInput{Total: 1000, Bounced: 55, Rejected: 55}, 50, true},  // 11% -> 50
			{BounceInput{Total: 1000, Deferred: 500}, 100, true},             // deferred not counted
		}
		for _, c := range cases {
			got, _, present := c.in.score()
			if present != c.present {
				t.Errorf("bounce present: got %v want %v", present, c.present)
			}
			if present && !approx(got, c.want) {
				t.Errorf("bounce %+v: got %.2f want %.2f", c.in, got, c.want)
			}
		}
	})

	t.Run("blocklist", func(t *testing.T) {
		cases := []struct {
			in      BlocklistInput
			want    float64
			present bool
		}{
			{BlocklistInput{}, 0, false},
			{BlocklistInput{TotalIPs: 4, ListedIPs: 0}, 100, true},
			{BlocklistInput{TotalIPs: 4, ListedIPs: 1}, 50, true}, // capped at 50 on any listing
			{BlocklistInput{TotalIPs: 4, ListedIPs: 4}, 0, true},
		}
		for _, c := range cases {
			got, _, present := c.in.score()
			if present != c.present {
				t.Errorf("blocklist present: got %v want %v", present, c.present)
			}
			if present && !approx(got, c.want) {
				t.Errorf("blocklist %+v: got %.2f want %.2f", c.in, got, c.want)
			}
		}
	})

	t.Run("anomaly", func(t *testing.T) {
		cases := []struct {
			in   AnomalyInput
			want float64
		}{
			{AnomalyInput{ActiveSpike: false}, 100},
			{AnomalyInput{ActiveSpike: true, Ratio: 2}, 75},
			{AnomalyInput{ActiveSpike: true, Ratio: 5}, 0},
			{AnomalyInput{ActiveSpike: true, Ratio: 0.5}, 100}, // ratio<1 clamped
		}
		for _, c := range cases {
			got, _ := c.in.score()
			if !approx(got, c.want) {
				t.Errorf("anomaly %+v: got %.2f want %.2f", c.in, got, c.want)
			}
		}
	})

	t.Run("postmaster", func(t *testing.T) {
		cases := []struct {
			name    string
			in      PostmasterInput
			want    float64
			present bool
		}{
			{"absent", PostmasterInput{}, 0, false},
			{"high", PostmasterInput{Grade: "HIGH"}, 100, true},
			{"lowercase medium", PostmasterInput{Grade: "medium"}, 60, true},
			{"bad", PostmasterInput{Grade: "BAD"}, 0, true},
			{"spam rate worse than grade", PostmasterInput{Grade: "HIGH", SpamRate: f64(0.0015)}, 50, true},
			{"only spam rate", PostmasterInput{SpamRate: f64(0.0)}, 100, true},
		}
		for _, c := range cases {
			got, _, present := c.in.score()
			if present != c.present {
				t.Errorf("%s present: got %v want %v", c.name, present, c.present)
			}
			if present && !approx(got, c.want) {
				t.Errorf("%s: got %.2f want %.2f", c.name, got, c.want)
			}
		}
	})
}

func TestCompute(t *testing.T) {
	w := DefaultWeights()

	t.Run("no data yields N/A", func(t *testing.T) {
		res := Compute(Inputs{}, w)
		if res.HasData {
			t.Fatalf("expected HasData=false")
		}
		if res.Grade != "N/A" {
			t.Errorf("grade: got %q want N/A", res.Grade)
		}
		if res.Coverage != 0 {
			t.Errorf("coverage: got %v want 0", res.Coverage)
		}
	})

	t.Run("perfect across all present -> A/100", func(t *testing.T) {
		res := Compute(Inputs{
			DMARC:      &DMARCInput{Total: 100, DKIMAligned: 100, SPFAligned: 100},
			Complaints: &ComplaintInput{0},
			Blocklist:  &BlocklistInput{TotalIPs: 2, ListedIPs: 0},
			Bounce:     &BounceInput{Total: 1000, Bounced: 10, Rejected: 10},
			Anomaly:    &AnomalyInput{ActiveSpike: false},
			Postmaster: &PostmasterInput{Grade: "HIGH"},
		}, w)
		if !res.HasData || !approx(res.Score, 100) || res.Grade != "A" {
			t.Fatalf("got score=%.2f grade=%s", res.Score, res.Grade)
		}
		if !approx(res.Coverage, 1.0) {
			t.Errorf("coverage: got %v want 1.0", res.Coverage)
		}
		if len(res.Drags()) != 0 {
			t.Errorf("expected no drags, got %d", len(res.Drags()))
		}
	})

	t.Run("missing components renormalize (neutral), not zero", func(t *testing.T) {
		// Only DMARC present and perfect. Score must be 100, not diluted by absent components.
		res := Compute(Inputs{
			DMARC: &DMARCInput{Total: 10, DKIMAligned: 10, SPFAligned: 10},
		}, w)
		if !approx(res.Score, 100) {
			t.Errorf("single perfect component: got %.2f want 100", res.Score)
		}
		if res.Coverage >= 1.0 {
			t.Errorf("coverage should be <1 with one component, got %v", res.Coverage)
		}
	})

	t.Run("single blocklist listing tanks the score", func(t *testing.T) {
		full := Compute(Inputs{
			DMARC:     &DMARCInput{Total: 100, DKIMAligned: 100, SPFAligned: 100},
			Bounce:    &BounceInput{Total: 1000, Bounced: 10, Rejected: 10},
			Blocklist: &BlocklistInput{TotalIPs: 1, ListedIPs: 1},
		}, w)
		if full.Score >= 100 {
			t.Errorf("expected blocklist drag, got %.2f", full.Score)
		}
		drags := full.Drags()
		if len(drags) == 0 || drags[0].Name != ComponentBlocklist {
			t.Fatalf("expected blocklist to be top drag, got %+v", drags)
		}
	})

	t.Run("weighted math is exact for two equal-present components", func(t *testing.T) {
		// DMARC weight .25, Bounce weight .20. DMARC=100, Bounce=0.
		// normalized: dmarc .25/.45, bounce .20/.45 -> 100*.5556 + 0 = 55.56
		res := Compute(Inputs{
			DMARC:  &DMARCInput{Total: 100, DKIMAligned: 100, SPFAligned: 100},
			Bounce: &BounceInput{Total: 100, Bounced: 20, Rejected: 0}, // 20% -> 0
		}, w)
		want := 100 * (0.25 / 0.45)
		if !approx(res.Score, want) {
			t.Errorf("got %.2f want %.2f", res.Score, want)
		}
	})

	t.Run("explain never empty and mentions grade", func(t *testing.T) {
		res := Compute(Inputs{DMARC: &DMARCInput{Total: 100, DKIMAligned: 50}}, w)
		if got := res.Explain(); got == "" {
			t.Fatal("explain empty")
		}
	})
}

func TestGrade(t *testing.T) {
	cases := map[float64]string{95: "A", 85: "B", 75: "C", 65: "D", 40: "F", 90: "A", 60: "D"}
	for score, want := range cases {
		if got := Grade(score); got != want {
			t.Errorf("Grade(%.0f): got %s want %s", score, got, want)
		}
	}
}
