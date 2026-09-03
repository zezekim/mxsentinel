package api

import (
	"net/http"
	"time"
)

// handleDeliveryOutcomes (GET /v1/analytics/delivery-outcomes) reports how MESSAGES
// ended up, not how individual delivery attempts went.
//
// The overview pairs this with the event-level series so the two rates can be shown
// side by side and labelled for what they are: retries make the event-level delivered
// share look far worse than the share of mail that actually arrives.
//
// Defaults to the last 30 days.
func (s *Server) handleDeliveryOutcomes(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC()
	until := parseTimeParam(r, "until")
	if until.IsZero() {
		until = now
	}
	since := parseTimeParam(r, "since")
	if since.IsZero() {
		since = until.Add(-30 * 24 * time.Hour)
	}
	if !since.Before(until) {
		writeError(w, http.StatusBadRequest, "bad_request", "since must be before until")
		return
	}

	o, err := s.ch.DeliveryOutcomes(r.Context(), s.tenant(r), since, until)
	if err != nil {
		s.log.Error("delivery outcomes", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to load delivery outcomes")
		return
	}

	rate := func(n uint64) float64 {
		if o.Messages == 0 {
			return 0
		}
		return float64(n) / float64(o.Messages)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"messages":                o.Messages,
		"first_attempt_delivered": o.FirstAttemptDelivered,
		"delivered_final":         o.DeliveredFinal,
		"permanently_failed":      o.PermanentlyFailed,
		"still_retrying":          o.StillRetrying,
		"ever_deferred":           o.EverDeferred,
		"deferral_events":         o.DeferralEvents,
		"first_attempt_rate":      rate(o.FirstAttemptDelivered),
		"final_delivery_rate":     rate(o.DeliveredFinal),
		"permanent_failure_rate":  rate(o.PermanentlyFailed),
		"since":                   since.UTC().Format(time.RFC3339),
		"until":                   until.UTC().Format(time.RFC3339),
	})
}
