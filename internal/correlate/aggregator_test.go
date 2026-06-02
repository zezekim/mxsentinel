package correlate

import (
	"testing"
	"time"
)

func TestAggregatorDetectsSpike(t *testing.T) {
	a := NewAggregator()
	now := time.Date(2026, 6, 3, 11, 0, 0, 0, time.UTC)
	k := Key{TenantID: "t1", Domain: "example.com", Provider: "microsoft"}

	// Baseline 30 min ago: mostly delivered.
	base := now.Add(-30 * time.Minute)
	for i := 0; i < 98; i++ {
		a.Observe(k, base, false, "")
	}
	for i := 0; i < 2; i++ {
		a.Observe(k, base, true, ReasonReputation)
	}

	// Recent 1 min ago: heavy auth rejections.
	recent := now.Add(-1 * time.Minute)
	for i := 0; i < 60; i++ {
		a.Observe(k, recent, false, "")
	}
	for i := 0; i < 40; i++ {
		a.Observe(k, recent, true, ReasonAuth)
	}

	spikes := a.Evaluate(now, 5, 55, DefaultSpikeConfig())
	if len(spikes) != 1 {
		t.Fatalf("expected 1 spike, got %d", len(spikes))
	}
	s := spikes[0]
	if s.Domain != "example.com" || s.Provider != "microsoft" {
		t.Errorf("wrong key: %+v", s)
	}
	if s.DominantReason != ReasonAuth {
		t.Errorf("dominant reason = %q, want auth", s.DominantReason)
	}
	if s.Rejections != 40 {
		t.Errorf("rejections = %d, want 40", s.Rejections)
	}
}

func TestAggregatorNoSpikeWhenHealthy(t *testing.T) {
	a := NewAggregator()
	now := time.Date(2026, 6, 3, 11, 0, 0, 0, time.UTC)
	k := Key{TenantID: "t1", Domain: "ok.com", Provider: "google"}
	for i := 0; i < 100; i++ {
		a.Observe(k, now.Add(-1*time.Minute), false, "")
	}
	if spikes := a.Evaluate(now, 5, 55, DefaultSpikeConfig()); len(spikes) != 0 {
		t.Errorf("expected no spikes, got %d", len(spikes))
	}
}

func TestAggregatorPrune(t *testing.T) {
	a := NewAggregator()
	now := time.Date(2026, 6, 3, 11, 0, 0, 0, time.UTC)
	k := Key{TenantID: "t1", Domain: "old.com", Provider: "google"}
	a.Observe(k, now.Add(-100*time.Minute), true, ReasonReputation)
	a.Prune(now, 70)
	// After pruning, the stale key is gone entirely.
	if _, ok := a.data[k]; ok {
		t.Error("expected stale key to be pruned")
	}
}
