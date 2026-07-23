package alertchannels

import (
	"context"
	"log/slog"
	"time"
)

// Delivery statuses recorded in the alert_deliveries log.
const (
	StatusSent            = "sent"
	StatusFailed          = "failed"
	StatusSkippedThrottle = "skipped_throttle"
	StatusSkippedDedup    = "skipped_dedup"
)

// Channel is the decrypted view of an alert_channels row the dispatcher operates on.
// Config holds the JSON config with secrets already decrypted (see OpenConfig).
type Channel struct {
	ID       string
	TenantID string
	Type     string
	Name     string
	Config   []byte
	Enabled  bool
}

// DeliveryStore backs the dispatcher's dedup and throttle decisions and records outcomes.
// Implemented by the Postgres store; faked in tests.
type DeliveryStore interface {
	// DeliveredFor reports whether a successful ("sent") delivery already exists for this
	// (channel, alertRef) within the given window. Drives dedup.
	DeliveredFor(ctx context.Context, channelID, alertRef string, within time.Duration) (bool, error)
	// LastSentToChannel reports whether ANY successful delivery to this channel happened
	// within the given window. Drives per-channel throttling.
	LastSentToChannel(ctx context.Context, channelID string, within time.Duration) (bool, error)
	// Record appends a delivery-log row.
	Record(ctx context.Context, channelID, alertRef, status, errMsg string) error
}

// Result is the per-channel outcome of a Dispatch call.
type Result struct {
	ChannelID string
	Type      string
	Status    string // one of the Status* constants
	Err       string
}

// Dispatcher fans a Notification out to a set of channels, applying dedup and per-channel
// throttling before delegating the actual send to the matching Notifier driver. All
// non-determinism (clock, persistence, network) is injected so behavior is testable.
type Dispatcher struct {
	Notifiers map[string]Notifier // channel type -> driver
	Store     DeliveryStore
	Throttle  time.Duration // min gap between two sends to the same channel (0 disables)
	Dedup     time.Duration // window in which a repeated alertRef to a channel is suppressed
	Log       *slog.Logger
}

// Dispatch delivers n to every enabled channel. For each channel it:
//  1. skips (dedup) if this exact alertRef was already delivered to the channel within
//     the Dedup window;
//  2. skips (throttle) if any alert was delivered to the channel within the Throttle
//     window — this is what stops a flapping alert from spamming a channel;
//  3. otherwise sends via the driver and records the outcome.
//
// Test notifications (n.Test) bypass dedup and throttle so an operator can always verify a
// channel. Every decision is written to the delivery log (including skips) for audit.
func (d *Dispatcher) Dispatch(ctx context.Context, channels []Channel, n Notification) []Result {
	results := make([]Result, 0, len(channels))
	for _, ch := range channels {
		results = append(results, d.dispatchOne(ctx, ch, n))
	}
	return results
}

func (d *Dispatcher) dispatchOne(ctx context.Context, ch Channel, n Notification) Result {
	res := Result{ChannelID: ch.ID, Type: ch.Type}

	if !ch.Enabled {
		res.Status = StatusSkippedThrottle
		res.Err = "channel disabled"
		return res
	}

	if !n.Test {
		if d.Dedup > 0 {
			if seen, err := d.Store.DeliveredFor(ctx, ch.ID, n.AlertRef, d.Dedup); err != nil {
				d.log().Warn("dedup check failed; proceeding", "channel_id", ch.ID, "err", err)
			} else if seen {
				res.Status = StatusSkippedDedup
				d.record(ctx, ch.ID, n.AlertRef, StatusSkippedDedup, "")
				return res
			}
		}
		if d.Throttle > 0 {
			if recent, err := d.Store.LastSentToChannel(ctx, ch.ID, d.Throttle); err != nil {
				d.log().Warn("throttle check failed; proceeding", "channel_id", ch.ID, "err", err)
			} else if recent {
				res.Status = StatusSkippedThrottle
				d.record(ctx, ch.ID, n.AlertRef, StatusSkippedThrottle, "")
				return res
			}
		}
	}

	notifier, ok := d.Notifiers[ch.Type]
	if !ok {
		res.Status = StatusFailed
		res.Err = "no driver for channel type " + ch.Type
		d.record(ctx, ch.ID, n.AlertRef, StatusFailed, res.Err)
		return res
	}

	cfg, err := DecodeConfig(ch.Config)
	if err != nil {
		res.Status = StatusFailed
		res.Err = "bad config"
		d.record(ctx, ch.ID, n.AlertRef, StatusFailed, err.Error())
		return res
	}

	if err := notifier.Send(ctx, n, cfg); err != nil {
		res.Status = StatusFailed
		res.Err = err.Error()
		d.record(ctx, ch.ID, n.AlertRef, StatusFailed, err.Error())
		return res
	}

	res.Status = StatusSent
	d.record(ctx, ch.ID, n.AlertRef, StatusSent, "")
	return res
}

func (d *Dispatcher) record(ctx context.Context, channelID, alertRef, status, errMsg string) {
	if d.Store == nil {
		return
	}
	if err := d.Store.Record(ctx, channelID, alertRef, status, errMsg); err != nil {
		d.log().Warn("record delivery failed", "channel_id", channelID, "status", status, "err", err)
	}
}

func (d *Dispatcher) log() *slog.Logger {
	if d.Log != nil {
		return d.Log
	}
	return slog.Default()
}
