package api

import (
	"encoding/json"
	"net/http"
	"strings"

	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
)

// Bounds for the monitoring tuning knobs. A value of 0 (or "" for strings) always means
// "unset" and is left alone; only non-zero values are range-checked here. The daemons apply
// their own defaults when a field is unset.
const (
	tuningMaxIntervalSecs = 86400 // 24h — no cadence should be slower than daily
	tuningMaxTimeoutSecs  = 300   // 5m — generous ceiling for a single network op
	tuningMaxWarnDays     = 365
	tuningMaxEHLOLen      = 255
)

type monitoringTuningJSON struct {
	TLSRPT struct {
		IntervalSecs int `json:"interval_secs"`
	} `json:"tlsrpt"`
	MTASTS struct {
		IntervalSecs    int `json:"interval_secs"`
		CertWarnDays    int `json:"cert_warn_days"`
		CertTimeoutSecs int `json:"cert_timeout_secs"`
		HTTPTimeoutSecs int `json:"http_timeout_secs"`
	} `json:"mtasts"`
	Probe struct {
		IntervalSecs       int    `json:"interval_secs"`
		ConnectTimeoutSecs int    `json:"connect_timeout_secs"`
		CommandTimeoutSecs int    `json:"command_timeout_secs"`
		CertWarnDays       int    `json:"cert_warn_days"`
		EHLOName           string `json:"ehlo_name"`
		TLSInsecure        bool   `json:"tls_insecure"`
		CheckResponse      bool   `json:"check_response"`
	} `json:"probe"`
	BIMI struct {
		IntervalSecs     int `json:"interval_secs"`
		FetchTimeoutSecs int `json:"fetch_timeout_secs"`
	} `json:"bimi"`
}

func toMonitoringTuningJSON(m pgstore.MonitoringTuning) monitoringTuningJSON {
	var j monitoringTuningJSON
	j.TLSRPT.IntervalSecs = m.TLSRPT.IntervalSecs
	j.MTASTS.IntervalSecs = m.MTASTS.IntervalSecs
	j.MTASTS.CertWarnDays = m.MTASTS.CertWarnDays
	j.MTASTS.CertTimeoutSecs = m.MTASTS.CertTimeoutSecs
	j.MTASTS.HTTPTimeoutSecs = m.MTASTS.HTTPTimeoutSecs
	j.Probe.IntervalSecs = m.Probe.IntervalSecs
	j.Probe.ConnectTimeoutSecs = m.Probe.ConnectTimeoutSecs
	j.Probe.CommandTimeoutSecs = m.Probe.CommandTimeoutSecs
	j.Probe.CertWarnDays = m.Probe.CertWarnDays
	j.Probe.EHLOName = m.Probe.EHLOName
	j.Probe.TLSInsecure = m.Probe.TLSInsecure
	j.Probe.CheckResponse = m.Probe.CheckResponse
	j.BIMI.IntervalSecs = m.BIMI.IntervalSecs
	j.BIMI.FetchTimeoutSecs = m.BIMI.FetchTimeoutSecs
	return j
}

func fromMonitoringTuningJSON(j monitoringTuningJSON) pgstore.MonitoringTuning {
	var m pgstore.MonitoringTuning
	m.TLSRPT.IntervalSecs = j.TLSRPT.IntervalSecs
	m.MTASTS.IntervalSecs = j.MTASTS.IntervalSecs
	m.MTASTS.CertWarnDays = j.MTASTS.CertWarnDays
	m.MTASTS.CertTimeoutSecs = j.MTASTS.CertTimeoutSecs
	m.MTASTS.HTTPTimeoutSecs = j.MTASTS.HTTPTimeoutSecs
	m.Probe.IntervalSecs = j.Probe.IntervalSecs
	m.Probe.ConnectTimeoutSecs = j.Probe.ConnectTimeoutSecs
	m.Probe.CommandTimeoutSecs = j.Probe.CommandTimeoutSecs
	m.Probe.CertWarnDays = j.Probe.CertWarnDays
	m.Probe.EHLOName = j.Probe.EHLOName
	m.Probe.TLSInsecure = j.Probe.TLSInsecure
	m.Probe.CheckResponse = j.Probe.CheckResponse
	m.BIMI.IntervalSecs = j.BIMI.IntervalSecs
	m.BIMI.FetchTimeoutSecs = j.BIMI.FetchTimeoutSecs
	return m
}

// handleGetMonitoringTuning returns the tenant's monitoring daemon tuning (read scope).
// Unset knobs are returned as 0 / "" so the dashboard can render them as "default".
func (s *Server) handleGetMonitoringTuning(w http.ResponseWriter, r *http.Request) {
	m, err := s.pg.GetMonitoringTuning(r.Context(), s.tenant(r))
	if err != nil {
		s.log.Error("get monitoring tuning", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to read monitoring tuning")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tuning": toMonitoringTuningJSON(m)})
}

// secsField validates a "0 = unset, otherwise 1..maxVal" seconds knob.
func validTuningRange(v, maxVal int) bool {
	return v == 0 || (v >= 1 && v <= maxVal)
}

// handleUpdateMonitoringTuning replaces the tenant's monitoring tuning (admin scope).
func (s *Server) handleUpdateMonitoringTuning(w http.ResponseWriter, r *http.Request) {
	var req monitoringTuningJSON
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	req.Probe.EHLOName = strings.TrimSpace(req.Probe.EHLOName)

	intervals := map[string]int{
		"tlsrpt.interval_secs": req.TLSRPT.IntervalSecs,
		"mtasts.interval_secs": req.MTASTS.IntervalSecs,
		"probe.interval_secs":  req.Probe.IntervalSecs,
		"bimi.interval_secs":   req.BIMI.IntervalSecs,
	}
	for name, v := range intervals {
		if !validTuningRange(v, tuningMaxIntervalSecs) {
			writeError(w, http.StatusBadRequest, "bad_request", name+" must be 0 (default) or between 1 and 86400 seconds")
			return
		}
	}

	timeouts := map[string]int{
		"mtasts.cert_timeout_secs":   req.MTASTS.CertTimeoutSecs,
		"mtasts.http_timeout_secs":   req.MTASTS.HTTPTimeoutSecs,
		"probe.connect_timeout_secs": req.Probe.ConnectTimeoutSecs,
		"probe.command_timeout_secs": req.Probe.CommandTimeoutSecs,
		"bimi.fetch_timeout_secs":    req.BIMI.FetchTimeoutSecs,
	}
	for name, v := range timeouts {
		if !validTuningRange(v, tuningMaxTimeoutSecs) {
			writeError(w, http.StatusBadRequest, "bad_request", name+" must be 0 (default) or between 1 and 300 seconds")
			return
		}
	}

	warnDays := map[string]int{
		"mtasts.cert_warn_days": req.MTASTS.CertWarnDays,
		"probe.cert_warn_days":  req.Probe.CertWarnDays,
	}
	for name, v := range warnDays {
		if !validTuningRange(v, tuningMaxWarnDays) {
			writeError(w, http.StatusBadRequest, "bad_request", name+" must be 0 (default) or between 1 and 365 days")
			return
		}
	}

	if len(req.Probe.EHLOName) > tuningMaxEHLOLen {
		writeError(w, http.StatusBadRequest, "bad_request", "probe.ehlo_name must be at most 255 characters")
		return
	}

	m := fromMonitoringTuningJSON(req)
	found, err := s.pg.UpdateMonitoringTuning(r.Context(), s.tenant(r), m)
	if err != nil {
		s.log.Error("update monitoring tuning", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to save monitoring tuning")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "tenant not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tuning": toMonitoringTuningJSON(m)})
}
