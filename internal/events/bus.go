package events

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/zezekim/mxsentinel/pkg/contracts"
)

// Bus is the JetStream-backed event bus client. It validates every published event
// against the embedded schemas before it hits the wire.
type Bus struct {
	nc  *nats.Conn
	js  jetstream.JetStream
	v   *Validator
	log *slog.Logger
}

// Connect dials NATS and wraps it with JetStream. The validator is used on publish (and
// on consume, for poison-message detection).
func Connect(url, name string, v *Validator, log *slog.Logger) (*Bus, error) {
	nc, err := nats.Connect(url,
		nats.Name(name),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("connect nats %q: %w", url, err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("init jetstream: %w", err)
	}
	return &Bus{nc: nc, js: js, v: v, log: log}, nil
}

// JS exposes the underlying JetStream handle for advanced use.
func (b *Bus) JS() jetstream.JetStream { return b.js }

// Close drains and closes the NATS connection.
func (b *Bus) Close() {
	if b.nc != nil {
		_ = b.nc.Drain()
	}
}

// EnsureStreams creates or updates every MX Sentinel stream. Idempotent; safe to call on
// every service start.
func (b *Bus) EnsureStreams(ctx context.Context) error {
	for name, subjects := range streamDefs {
		cfg := jetstream.StreamConfig{
			Name:      name,
			Subjects:  subjects,
			Retention: jetstream.LimitsPolicy,
			Storage:   jetstream.FileStorage,
			MaxAge:    streamMaxAge(name),
		}
		if _, err := b.js.CreateOrUpdateStream(ctx, cfg); err != nil {
			return fmt.Errorf("ensure stream %s: %w", name, err)
		}
		b.log.Debug("stream ensured", "stream", name, "subjects", subjects)
	}
	return nil
}

// streamMaxAge gives high-volume telemetry a shorter retention than the lower-volume,
// higher-value streams (see docs/event-contracts.md). Rollups in ClickHouse outlive raw.
func streamMaxAge(stream string) time.Duration {
	switch stream {
	case "SMTP":
		return 14 * 24 * time.Hour
	case "DLQ":
		return 30 * 24 * time.Hour
	default:
		return 90 * 24 * time.Hour
	}
}

// Publish validates the envelope against its schema and publishes it to the family's
// tenant-scoped subject. Invalid events are rejected (never published).
func (b *Bus) Publish(ctx context.Context, ev *contracts.Envelope) error {
	raw, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}
	if err := b.v.ValidateRaw(string(ev.EventType), raw); err != nil {
		return fmt.Errorf("refusing to publish invalid event: %w", err)
	}
	subj := Subject(ev.EventType, ev.TenantID)
	if _, err := b.js.Publish(ctx, subj, raw); err != nil {
		return fmt.Errorf("publish to %s: %w", subj, err)
	}
	return nil
}
