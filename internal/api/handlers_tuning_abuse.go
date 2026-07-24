package api

import (
	"encoding/json"
	"net/http"

	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
)

// Abuse/bounce tuning is a group of NON-SECRET numeric/bool knobs for the authwatchd and
// bounced daemons. Every value is optional: a ZERO value (0 / false) means "unset — the daemon
// keeps using its env var / built-in default". The API therefore stores exactly what the
// operator entered (no defaulting on read) so that "leave blank to use default" round-trips.

type authwatchTuningJSON struct {
	Threshold      float64 `json:"threshold"`
	WindowSecs     int     `json:"window_secs"`
	CooldownSecs   int     `json:"cooldown_secs"`
	MinVolume      int     `json:"min_volume"`
	DistinctRcpt   int     `json:"distinct_rcpt"`
	BounceRate     float64 `json:"bounce_rate"`
	VolumeFactor   float64 `json:"volume_factor"`
	VolumeFloor    int     `json:"volume_floor"`
	OffHoursStart  int     `json:"offhours_start"`
	OffHoursEnd    int     `json:"offhours_end"`
	OffHoursRate   float64 `json:"offhours_rate"`
	OffHoursWeight float64 `json:"offhours_weight"`
	Autolock       bool    `json:"autolock"`
}

type bounceTuningJSON struct {
	IntervalSecs int `json:"interval_secs"`
	LookbackSecs int `json:"lookback_secs"`
	MaxRows      int `json:"max_rows"`
}

type abuseTuningJSON struct {
	Authwatch authwatchTuningJSON `json:"authwatch"`
	Bounce    bounceTuningJSON    `json:"bounce"`
}

func toAbuseTuningJSON(t pgstore.AbuseTuning) abuseTuningJSON {
	// Direct field copy — no defaulting: unset stays zero so the dashboard shows blanks and the
	// daemons decide the fallback (env/default) at startup.
	return abuseTuningJSON{
		Authwatch: authwatchTuningJSON(t.Authwatch),
		Bounce:    bounceTuningJSON(t.Bounce),
	}
}

func fromAbuseTuningJSON(j abuseTuningJSON) pgstore.AbuseTuning {
	return pgstore.AbuseTuning{
		Authwatch: pgstore.AuthwatchTuning(j.Authwatch),
		Bounce:    pgstore.BounceTuning(j.Bounce),
	}
}

// handleGetAbuseTuning returns the tenant's abuse/bounce tuning (read scope).
func (s *Server) handleGetAbuseTuning(w http.ResponseWriter, r *http.Request) {
	t, err := s.pg.GetAbuseTuning(r.Context(), s.tenant(r))
	if err != nil {
		s.log.Error("get abuse tuning", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to read tuning")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tuning_abuse": toAbuseTuningJSON(t)})
}

// validateAbuseTuning enforces sane ranges. Zero means "unset" and is always allowed; only
// explicitly-set (non-zero) values are range-checked, so partial configuration is fine.
func validateAbuseTuning(j abuseTuningJSON) (string, bool) {
	a := j.Authwatch
	if a.Threshold < 0 {
		return "authwatch.threshold must be >= 0", false
	}
	if a.WindowSecs < 0 {
		return "authwatch.window_secs must be >= 0", false
	}
	if a.CooldownSecs < 0 {
		return "authwatch.cooldown_secs must be >= 0", false
	}
	if a.MinVolume < 0 {
		return "authwatch.min_volume must be >= 0", false
	}
	if a.DistinctRcpt < 0 {
		return "authwatch.distinct_rcpt must be >= 0", false
	}
	if a.BounceRate < 0 || a.BounceRate > 1 {
		return "authwatch.bounce_rate must be between 0 and 1", false
	}
	if a.VolumeFactor < 0 {
		return "authwatch.volume_factor must be >= 0", false
	}
	if a.VolumeFloor < 0 {
		return "authwatch.volume_floor must be >= 0", false
	}
	// Off-hours band is a UTC hour window [0,24). 0 = unset per the group convention.
	if a.OffHoursStart < 0 || a.OffHoursStart > 23 {
		return "authwatch.offhours_start must be between 0 and 23", false
	}
	if a.OffHoursEnd < 0 || a.OffHoursEnd > 23 {
		return "authwatch.offhours_end must be between 0 and 23", false
	}
	if a.OffHoursRate < 0 || a.OffHoursRate > 1 {
		return "authwatch.offhours_rate must be between 0 and 1", false
	}
	if a.OffHoursWeight < 0 {
		return "authwatch.offhours_weight must be >= 0", false
	}

	b := j.Bounce
	if b.IntervalSecs < 0 {
		return "bounce.interval_secs must be >= 0", false
	}
	if b.LookbackSecs < 0 {
		return "bounce.lookback_secs must be >= 0", false
	}
	if b.MaxRows < 0 {
		return "bounce.max_rows must be >= 0", false
	}
	// When both are set, the lookback must comfortably exceed the interval or ticks can miss
	// bounces between passes (see internal/bounce DefaultLookback rationale).
	if b.IntervalSecs > 0 && b.LookbackSecs > 0 && b.LookbackSecs < b.IntervalSecs {
		return "bounce.lookback_secs must be >= bounce.interval_secs", false
	}
	return "", true
}

// handleUpdateAbuseTuning replaces the tenant's abuse/bounce tuning (admin scope).
func (s *Server) handleUpdateAbuseTuning(w http.ResponseWriter, r *http.Request) {
	var req abuseTuningJSON
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if msg, ok := validateAbuseTuning(req); !ok {
		writeError(w, http.StatusBadRequest, "bad_request", msg)
		return
	}

	t := fromAbuseTuningJSON(req)
	found, err := s.pg.UpdateAbuseTuning(r.Context(), s.tenant(r), t)
	if err != nil {
		s.log.Error("update abuse tuning", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to save tuning")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "tenant not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tuning_abuse": toAbuseTuningJSON(t)})
}
