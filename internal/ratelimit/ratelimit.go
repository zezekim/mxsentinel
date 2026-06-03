// Package ratelimit provides a fixed-window rate limiter over a pluggable counter, so the
// API can throttle per tenant. The counter is an interface: an in-memory implementation
// (single instance) and a Redis-backed one (shared across apid instances) both satisfy it.
package ratelimit

import (
	"context"
	"sync"
	"time"
)

// Counter increments a key's count and ensures it expires after window (the first
// increment in a window sets the TTL). Returns the current count.
type Counter interface {
	Incr(ctx context.Context, key string, window time.Duration) (int64, error)
}

// Limiter enforces `limit` events per `window` per key.
type Limiter struct {
	c      Counter
	limit  int
	window time.Duration
}

// New builds a Limiter.
func New(c Counter, limit int, window time.Duration) *Limiter {
	return &Limiter{c: c, limit: limit, window: window}
}

// Allow records one event for key and reports whether it is within the limit. On counter
// errors it fails OPEN (allows) so a limiter outage never takes the API down.
func (l *Limiter) Allow(ctx context.Context, key string) (allowed bool, count int64, err error) {
	n, err := l.c.Incr(ctx, "rl:"+key, l.window)
	if err != nil {
		return true, 0, err
	}
	return n <= int64(l.limit), n, nil
}

// MemCounter is an in-process fixed-window counter. Now is overridable for tests.
type MemCounter struct {
	mu  sync.Mutex
	m   map[string]memEntry
	Now func() time.Time
}

type memEntry struct {
	count int64
	exp   time.Time
}

// NewMemCounter returns an empty in-memory counter.
func NewMemCounter() *MemCounter {
	return &MemCounter{m: make(map[string]memEntry), Now: time.Now}
}

// Incr increments key, resetting the window once the previous one has expired.
func (c *MemCounter) Incr(_ context.Context, key string, window time.Duration) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.Now()
	e, ok := c.m[key]
	if !ok || now.After(e.exp) {
		e = memEntry{exp: now.Add(window)}
	}
	e.count++
	c.m[key] = e
	return e.count, nil
}
