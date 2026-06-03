// Package redisstore provides the Redis client and a Deduper used to make event
// consumers idempotent (see internal/events). Nothing here is a source of truth — every
// key is TTL'd and reconstructable.
package redisstore

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/zezekim/mxsentinel/internal/config"
)

// Store wraps a Redis client.
type Store struct {
	Client *redis.Client
}

// RateCounter is a Redis-backed fixed-window counter; it satisfies ratelimit.Counter and
// lets multiple apid instances share one limit.
type RateCounter struct {
	client *redis.Client
}

// RateCounter returns a counter over this store's client.
func (s *Store) RateCounter() *RateCounter { return &RateCounter{client: s.Client} }

// Incr increments key and, on the first increment of a window, sets its TTL.
func (c *RateCounter) Incr(ctx context.Context, key string, window time.Duration) (int64, error) {
	n, err := c.client.Incr(ctx, key).Result()
	if err != nil {
		return 0, fmt.Errorf("ratelimit incr: %w", err)
	}
	if n == 1 {
		_ = c.client.Expire(ctx, key, window).Err()
	}
	return n, nil
}

// New connects to Redis and verifies connectivity.
func New(ctx context.Context, cfg config.RedisConfig) (*Store, error) {
	c := redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	})
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := c.Ping(pingCtx).Err(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	return &Store{Client: c}, nil
}

// Close closes the client.
func (s *Store) Close() error { return s.Client.Close() }

// Deduper records seen event ids so consumers can drop duplicates. It satisfies
// events.Deduper.
type Deduper struct {
	client *redis.Client
	prefix string
}

// NewDeduper builds a Deduper over the store's client.
func (s *Store) NewDeduper(prefix string) *Deduper {
	if prefix == "" {
		prefix = "dedupe:"
	}
	return &Deduper{client: s.Client, prefix: prefix}
}

// Seen records the key and reports whether it was already present (a duplicate). It uses
// SET NX: the first writer wins and sees false; subsequent callers see true until the TTL
// expires.
func (d *Deduper) Seen(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	set, err := d.client.SetNX(ctx, d.prefix+key, "1", ttl).Result()
	if err != nil {
		return false, fmt.Errorf("dedupe setnx: %w", err)
	}
	// set == true  -> we just stored it -> not seen before
	// set == false -> key already existed -> duplicate
	return !set, nil
}
