package api

import (
	"net/http"
	"time"
)

type seriesPointJSON struct {
	Bucket        string  `json:"bucket"`
	Delivered     uint64  `json:"delivered"`
	Deferred      uint64  `json:"deferred"`
	Bounced       uint64  `json:"bounced"`
	Rejected      uint64  `json:"rejected"`
	Total         uint64  `json:"total"`
	DeliveredRate float64 `json:"delivered_rate"`
}

// validGranularity mirrors the bucket sizes the ClickHouse store understands. Kept here
// so a bad value is a 400 from the handler rather than a 500 surfaced from the store.
var validGranularity = map[string]bool{"5m": true, "hour": true, "day": true}

// pickGranularity chooses a bucket size that keeps a window to a chart-sized number of
// points: 5-minute buckets stay readable for about a day, hourly to about a fortnight,
// daily beyond that.
func pickGranularity(since, until time.Time) string {
	span := until.Sub(since)
	switch {
	case span <= 24*time.Hour:
		return "5m"
	case span <= 14*24*time.Hour:
		return "hour"
	default:
		return "day"
	}
}

// handleDeliverabilityTimeseries (GET /v1/analytics/timeseries) returns tenant-wide
// outcome counts bucketed over time, for the overview dashboard's trend chart.
//
// Defaults to the last 30 days. "granularity" (5m|hour|day) overrides the size picked
// from the window. Empty buckets are omitted — the rollup has no row for a period with
// no traffic — so consumers plotting a continuous axis need to fill the gaps.
func (s *Server) handleDeliverabilityTimeseries(w http.ResponseWriter, r *http.Request) {
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

	granularity := r.URL.Query().Get("granularity")
	if granularity == "" {
		granularity = pickGranularity(since, until)
	} else if !validGranularity[granularity] {
		writeError(w, http.StatusBadRequest, "bad_request", "granularity must be one of: 5m, hour, day")
		return
	}

	points, err := s.ch.DeliverabilitySeries(r.Context(), s.tenant(r), since, until, granularity)
	if err != nil {
		s.log.Error("deliverability timeseries", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to load timeseries")
		return
	}

	out := make([]seriesPointJSON, 0, len(points))
	for _, p := range points {
		rate := 0.0
		if p.Total > 0 {
			rate = float64(p.Delivered) / float64(p.Total)
		}
		out = append(out, seriesPointJSON{
			Bucket:    p.Bucket.UTC().Format(time.RFC3339),
			Delivered: p.Delivered, Deferred: p.Deferred,
			Bounced: p.Bounced, Rejected: p.Rejected,
			Total: p.Total, DeliveredRate: rate,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"points":      out,
		"granularity": granularity,
		"since":       since.UTC().Format(time.RFC3339),
		"until":       until.UTC().Format(time.RFC3339),
	})
}
