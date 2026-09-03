package clickhouse

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// MessageOutcomes counts MESSAGES by their final fate, as opposed to the per-event
// counts everything else in analytics reports.
//
// The distinction matters a great deal here. smtp_events holds one row per delivery
// ATTEMPT, and a deferred message is retried until it delivers, hard-fails, or ages
// out — so a single stuck message can contribute hundreds of "deferred" rows. Counting
// events therefore understates delivery badly: over a representative 30 days this
// tenant logged 280k deferral events from just 9.4k messages (~30 retries each, worst
// case 333), dragging the event-level delivered share down to ~29% while ~80% of
// messages actually did get delivered.
type MessageOutcomes struct {
	Messages              uint64
	FirstAttemptDelivered uint64
	DeliveredFinal        uint64
	PermanentlyFailed     uint64
	StillRetrying         uint64
	EverDeferred          uint64
	DeferralEvents        uint64
}

// DeliveryOutcomes collapses the event stream to one row per message (by relay queue
// id) and reports how those messages ended up. since/until are optional; the zero value
// means unbounded on that side.
//
// 'received' events are excluded: they record acceptance into the relay, not an
// outbound delivery attempt, so counting them would inflate the denominator with
// messages that have not been attempted yet.
func (s *Store) DeliveryOutcomes(ctx context.Context, tenantID string, since, until time.Time) (MessageOutcomes, error) {
	var where strings.Builder
	where.WriteString("tenant_id = ? AND queue_id != '' AND event_type != 'received'")
	args := []any{tenantID}
	if !since.IsZero() {
		where.WriteString(" AND event_time >= ?")
		args = append(args, since)
	}
	if !until.IsZero() {
		where.WriteString(" AND event_time <= ?")
		args = append(args, until)
	}

	// The inner query is one row per message: what its first and last attempt said,
	// whether it ever succeeded, and how many times it was deferred.
	query := fmt.Sprintf(`
		WITH per_msg AS (
			SELECT
				queue_id,
				argMin(event_type, event_time) AS first_type,
				argMax(event_type, event_time) AS final_type,
				maxIf(1, event_type = 'delivered') AS got_delivered,
				countIf(event_type = 'deferred') AS defers
			FROM smtp_events
			WHERE %s
			GROUP BY queue_id
		)
		SELECT
			count(),
			countIf(first_type = 'delivered'),
			sum(got_delivered),
			countIf(got_delivered = 0 AND final_type IN ('bounced', 'rejected')),
			countIf(got_delivered = 0 AND final_type = 'deferred'),
			countIf(defers > 0),
			sum(defers)
		FROM per_msg`, where.String())

	var o MessageOutcomes
	row := s.conn.QueryRow(ctx, query, args...)
	if err := row.Scan(
		&o.Messages,
		&o.FirstAttemptDelivered,
		&o.DeliveredFinal,
		&o.PermanentlyFailed,
		&o.StillRetrying,
		&o.EverDeferred,
		&o.DeferralEvents,
	); err != nil {
		return MessageOutcomes{}, fmt.Errorf("delivery outcomes: %w", err)
	}
	return o, nil
}
