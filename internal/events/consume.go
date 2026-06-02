package events

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/zezekim/mxsentinel/pkg/contracts"
)

// Handler processes a decoded, schema-valid event. Returning an error triggers redelivery
// (Nak); returning nil acknowledges the message. Handlers must be idempotent regardless,
// because delivery is at-least-once.
type Handler func(ctx context.Context, ev *contracts.Envelope) error

// Deduper records whether an event id has been seen, making consumers idempotent.
// Seen reports true if the id was already recorded (i.e. this is a duplicate).
type Deduper interface {
	Seen(ctx context.Context, key string, ttl time.Duration) (bool, error)
}

// ConsumeOptions configures a durable consumer.
type ConsumeOptions struct {
	Stream        string        // JetStream stream name, e.g. "SMTP"
	Durable       string        // durable consumer name (stable across restarts)
	FilterSubject string        // subject filter, e.g. "mxs.smtp.>" or per-tenant
	Deduper       Deduper       // optional; nil disables dedupe
	DedupeTTL     time.Duration // how long to remember an event id (default 10m)
	MaxDeliver    int           // redelivery attempts before giving up (default 5)
}

// Consume starts a push consumer that decodes, dedupes, validates, and dispatches events.
// Malformed or schema-invalid messages are routed to the DLQ and acked (poison messages
// are never redelivered forever). The returned ConsumeContext is stopped to unsubscribe.
func (b *Bus) Consume(ctx context.Context, opts ConsumeOptions, h Handler) (jetstream.ConsumeContext, error) {
	if opts.DedupeTTL == 0 {
		opts.DedupeTTL = 10 * time.Minute
	}
	if opts.MaxDeliver == 0 {
		opts.MaxDeliver = 5
	}

	stream, err := b.js.Stream(ctx, opts.Stream)
	if err != nil {
		return nil, fmt.Errorf("lookup stream %s: %w", opts.Stream, err)
	}
	cons, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Durable:       opts.Durable,
		FilterSubject: opts.FilterSubject,
		AckPolicy:     jetstream.AckExplicitPolicy,
		MaxDeliver:    opts.MaxDeliver,
		AckWait:       30 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("create consumer %s: %w", opts.Durable, err)
	}

	return cons.Consume(func(msg jetstream.Msg) {
		b.dispatch(ctx, opts, h, msg)
	})
}

func (b *Bus) dispatch(ctx context.Context, opts ConsumeOptions, h Handler, msg jetstream.Msg) {
	data := msg.Data()

	var ev contracts.Envelope
	if err := json.Unmarshal(data, &ev); err != nil {
		b.toDLQ(ctx, "unknown", data, fmt.Sprintf("malformed envelope: %v", err))
		_ = msg.Ack()
		return
	}

	// Idempotency: skip events we've already handled.
	if opts.Deduper != nil {
		seen, err := opts.Deduper.Seen(ctx, ev.EventID, opts.DedupeTTL)
		if err != nil {
			b.log.Warn("dedupe check failed; processing anyway", "event_id", ev.EventID, "err", err)
		} else if seen {
			_ = msg.Ack()
			return
		}
	}

	// Poison detection: invalid payloads go to the DLQ, not back on the queue.
	if err := b.v.ValidateRaw(string(ev.EventType), data); err != nil {
		b.toDLQ(ctx, ev.EventType.Family(), data, err.Error())
		_ = msg.Ack()
		return
	}

	if err := h(ctx, &ev); err != nil {
		b.log.Warn("handler error; will redeliver", "event_id", ev.EventID, "subject", msg.Subject(), "err", err)
		_ = msg.Nak()
		return
	}
	_ = msg.Ack()
}

func (b *Bus) toDLQ(ctx context.Context, family string, data []byte, reason string) {
	if family == "" {
		family = "unknown"
	}
	m := &nats.Msg{
		Subject: DLQSubject(family),
		Data:    data,
		Header:  nats.Header{},
	}
	m.Header.Set("Mxs-Dlq-Reason", reason)
	if _, err := b.js.PublishMsg(ctx, m); err != nil {
		b.log.Error("failed to route message to DLQ", "family", family, "reason", reason, "err", err)
	} else {
		b.log.Warn("routed message to DLQ", "family", family, "reason", reason)
	}
}
