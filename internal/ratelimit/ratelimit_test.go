package ratelimit

import (
	"context"
	"testing"
	"time"
)

func TestLimiterAllowsUpToLimit(t *testing.T) {
	l := New(NewMemCounter(), 3, time.Minute)
	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		if ok, _, _ := l.Allow(ctx, "tenant-1"); !ok {
			t.Fatalf("call %d should be allowed", i)
		}
	}
	if ok, n, _ := l.Allow(ctx, "tenant-1"); ok {
		t.Fatalf("4th call should be denied (count=%d)", n)
	}
}

func TestLimiterPerKeyIsolation(t *testing.T) {
	l := New(NewMemCounter(), 1, time.Minute)
	ctx := context.Background()
	if ok, _, _ := l.Allow(ctx, "a"); !ok {
		t.Fatal("first call for a should pass")
	}
	if ok, _, _ := l.Allow(ctx, "b"); !ok {
		t.Fatal("first call for b should pass (independent key)")
	}
	if ok, _, _ := l.Allow(ctx, "a"); ok {
		t.Fatal("second call for a should be denied")
	}
}

func TestLimiterWindowReset(t *testing.T) {
	mc := NewMemCounter()
	base := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	now := base
	mc.Now = func() time.Time { return now }

	l := New(mc, 2, time.Minute)
	ctx := context.Background()
	l.Allow(ctx, "t")
	l.Allow(ctx, "t")
	if ok, _, _ := l.Allow(ctx, "t"); ok {
		t.Fatal("3rd call within window should be denied")
	}

	now = base.Add(2 * time.Minute) // window elapsed
	if ok, _, _ := l.Allow(ctx, "t"); !ok {
		t.Fatal("call after window reset should be allowed")
	}
}

// errCounter always errors, to verify fail-open behavior.
type errCounter struct{}

func (errCounter) Incr(context.Context, string, time.Duration) (int64, error) {
	return 0, context.DeadlineExceeded
}

func TestLimiterFailsOpen(t *testing.T) {
	l := New(errCounter{}, 1, time.Minute)
	ok, _, err := l.Allow(context.Background(), "t")
	if !ok {
		t.Error("limiter should fail open (allow) when the counter errors")
	}
	if err == nil {
		t.Error("expected the counter error to be surfaced")
	}
}
