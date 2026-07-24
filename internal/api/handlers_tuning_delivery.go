package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
)

// Delivery & data tuning API (notifyd / sndsd / seedd / dmarcpulld / nlquery). Every field is
// NON-SECRET (durations-in-seconds, counts, one URL), so — unlike the provider/smarthost
// settings — nothing is sealed or write-only: GET returns the stored values verbatim and PUT
// echoes what it saved. Zero/blank means "use the daemon default"; values apply on daemon
// restart. Precedence when the daemon reads them: dashboard (DB) > env > built-in default.

// GET /v1/settings/tuning/delivery — read scope.
func (s *Server) handleGetDeliveryTuning(w http.ResponseWriter, r *http.Request) {
	m, err := s.pg.GetDeliveryTuning(r.Context(), s.tenant(r))
	if err != nil {
		s.log.Error("get delivery tuning", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to read delivery tuning")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tuning": m})
}

// PUT /v1/settings/tuning/delivery — admin scope.
func (s *Server) handleUpdateDeliveryTuning(w http.ResponseWriter, r *http.Request) {
	var req pgstore.DeliveryTuning
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	req.Notify.DashboardURL = strings.TrimSpace(req.Notify.DashboardURL)

	if msg := validateDeliveryTuning(req); msg != "" {
		writeError(w, http.StatusBadRequest, "bad_request", msg)
		return
	}

	found, err := s.pg.UpdateDeliveryTuning(r.Context(), s.tenant(r), req)
	if err != nil {
		s.log.Error("update delivery tuning", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to save delivery tuning")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "tenant not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tuning": req})
}

// validateDeliveryTuning enforces sane bounds. Each knob is optional: 0 means "unset / use the
// daemon default", so a field is only range-checked when it is set (non-zero). Returns "" when
// valid, else a human-readable message.
func validateDeliveryTuning(m pgstore.DeliveryTuning) string {
	checks := []struct {
		label    string
		v        int
		min, max int
	}{
		{"notify.poll_interval_secs", m.Notify.PollIntervalSecs, 1, 86400},
		{"notify.throttle_secs", m.Notify.ThrottleSecs, 1, 604800},
		{"notify.dedup_secs", m.Notify.DedupSecs, 1, 604800},
		{"notify.lookback_secs", m.Notify.LookbackSecs, 1, 604800},
		{"notify.http_timeout_secs", m.Notify.HTTPTimeoutSecs, 1, 600},
		{"snds.interval_secs", m.SNDS.IntervalSecs, 60, 604800},
		{"snds.jmrp_scan_interval_secs", m.SNDS.JMRPScanIntervalSecs, 1, 86400},
		{"snds.jmrp_complaint_threshold", m.SNDS.JMRPComplaintThreshold, 1, 1000000},
		{"seed.interval_secs", m.Seed.IntervalSecs, 1, 86400},
		{"seed.collect_window_secs", m.Seed.CollectWindowSecs, 60, 604800},
		{"dmarc_pull.interval_secs", m.DMARCPull.IntervalSecs, 60, 604800},
		{"dmarc_pull.lookback_days", m.DMARCPull.LookbackDays, 1, 365},
		{"nl.max_tools", m.NL.MaxTools, 1, 50},
	}
	for _, c := range checks {
		if c.v == 0 {
			continue // unset — keep the daemon default
		}
		if c.v < c.min || c.v > c.max {
			return fmt.Sprintf("%s must be between %d and %d", c.label, c.min, c.max)
		}
	}
	if len(m.Notify.DashboardURL) > 2048 {
		return "notify.dashboard_url is too long"
	}
	return ""
}
