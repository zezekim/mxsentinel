package relayfailover

import (
	"testing"
	"time"
)

func TestBreakerTripsOnSustainedTransientDefers(t *testing.T) {
	p := DefaultPolicy()
	b := NewBreaker()
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)

	// 70% of 100 attempts are 4xx defers — well over the 60% trip rate, above both floors.
	tr := b.Evaluate(now, Sample{Attempts: 100, Deferred4xx: 70}, p)
	if !tr.Changed || tr.To != StateOpen {
		t.Fatalf("expected trip to open, got %+v (state=%s)", tr, b.State)
	}
	if b.OpenedAt != now {
		t.Fatalf("OpenedAt not set: %v", b.OpenedAt)
	}
}

func TestBreakerDoesNotTripBelowFloors(t *testing.T) {
	p := DefaultPolicy()
	now := time.Now()

	cases := []struct {
		name string
		s    Sample
	}{
		{"too few attempts", Sample{Attempts: 10, Deferred4xx: 10}},       // rate 100% but < MinAttempts
		{"too few absolute defers", Sample{Attempts: 60, Deferred4xx: 5}}, // < MinDefers
		{"rate below trip", Sample{Attempts: 200, Deferred4xx: 40}},       // 20% < 60%
		{"no traffic", Sample{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := NewBreaker()
			tr := b.Evaluate(now, c.s, p)
			if tr.Changed || b.State != StateClosed {
				t.Fatalf("expected to stay closed, got %+v (state=%s)", tr, b.State)
			}
		})
	}
}

func TestBreakerRevertsAfterHold_TimeBasedNotRateBased(t *testing.T) {
	p := DefaultPolicy()
	p.HoldFor = 30 * time.Minute
	b := NewBreaker()
	t0 := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)

	// Trip it.
	if tr := b.Evaluate(t0, Sample{Attempts: 100, Deferred4xx: 80}, p); !tr.Changed || tr.To != StateOpen {
		t.Fatalf("expected trip, got %+v", tr)
	}

	// While open, a zero sample (no direct telemetry, because mail is failed over) must NOT
	// flap it back — recovery is time-based only.
	if tr := b.Evaluate(t0.Add(10*time.Minute), Sample{}, p); tr.Changed {
		t.Fatalf("breaker should stay open before HoldFor elapses, got %+v", tr)
	}
	if b.State != StateOpen {
		t.Fatalf("state=%s, want open", b.State)
	}

	// After HoldFor, it reverts to closed to re-probe directly.
	if tr := b.Evaluate(t0.Add(30*time.Minute), Sample{}, p); !tr.Changed || tr.To != StateClosed {
		t.Fatalf("expected revert to closed after hold, got %+v (state=%s)", tr, b.State)
	}
}

func TestBreakerRetripsIfStillThrottlingAfterRevert(t *testing.T) {
	p := DefaultPolicy()
	p.HoldFor = 15 * time.Minute
	b := NewBreaker()
	t0 := time.Now()

	b.Evaluate(t0, Sample{Attempts: 100, Deferred4xx: 90}, p)                           // open
	b.Evaluate(t0.Add(15*time.Minute), Sample{}, p)                                     // revert -> closed
	tr := b.Evaluate(t0.Add(16*time.Minute), Sample{Attempts: 100, Deferred4xx: 90}, p) // still bad
	if !tr.Changed || tr.To != StateOpen {
		t.Fatalf("expected re-trip, got %+v (state=%s)", tr, b.State)
	}
	if got := b.OpenedAt; got != t0.Add(16*time.Minute) {
		t.Fatalf("OpenedAt not refreshed on re-trip: %v", got)
	}
}

func TestSampleDeferRate(t *testing.T) {
	if got := (Sample{Attempts: 0, Deferred4xx: 5}).DeferRate(); got != 0 {
		t.Fatalf("zero attempts should give rate 0, got %v", got)
	}
	if got := (Sample{Attempts: 200, Deferred4xx: 50}).DeferRate(); got != 0.25 {
		t.Fatalf("rate = %v, want 0.25", got)
	}
}
