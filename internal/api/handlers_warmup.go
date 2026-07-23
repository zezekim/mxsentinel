package api

import (
	"encoding/json"
	"net/http"
	"time"

	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
)

// warmupRecommendedVolume returns the recommended send volume for a given day
// in a standard warm-up ramp schedule.
// Day 1=50, 2=100, 3=200, 4=500, 5=1000, 6=2000, 7=5000,
// then doubles each week (every 7 days) until target is reached.
func warmupRecommendedVolume(day int, target int) int {
	if day <= 0 {
		return 0
	}
	schedule := []int{50, 100, 200, 500, 1000, 2000, 5000}
	if day <= len(schedule) {
		v := schedule[day-1]
		if v > target {
			return target
		}
		return v
	}
	// Beyond day 7: double each week past day 7.
	// Week 1 ended at day 7 with 5000. Week 2 starts at day 8.
	weeksExtra := (day - 8) / 7 // 0-indexed additional weeks beyond the first post-ramp week
	v := 5000 * (1 << uint(weeksExtra+1))
	if v <= 0 || v > target {
		return target
	}
	return v
}

// ---- response shapes -------------------------------------------------------

type warmupPlanJSON struct {
	ID                string  `json:"id"`
	Name              string  `json:"name"`
	IPPool            string  `json:"ip_pool"`
	TargetDailyVolume int     `json:"target_daily_volume"`
	CurrentDay        int     `json:"current_day"`
	Stage             string  `json:"stage"`
	StartedAt         *string `json:"started_at"`
	CompletedAt       *string `json:"completed_at"`
	CreatedAt         string  `json:"created_at"`
}

type warmupDayStatJSON struct {
	DayNumber      int      `json:"day_number"`
	StatDate       string   `json:"stat_date"`
	Sent           int      `json:"sent"`
	Delivered      int      `json:"delivered"`
	Bounced        int      `json:"bounced"`
	Deferred       int      `json:"deferred"`
	AcceptanceRate *float64 `json:"acceptance_rate"`
}

type warmupPlanDetailJSON struct {
	warmupPlanJSON
	DayStats         []warmupDayStatJSON `json:"day_stats"`
	RecommendedToday int                 `json:"recommended_today"`
}

// ---- helpers ---------------------------------------------------------------

func formatWarmupPlan(p pgstore.WarmupPlan) warmupPlanJSON {
	var startedAt *string
	if !p.StartedAt.IsZero() {
		s := p.StartedAt.UTC().Format(time.RFC3339)
		startedAt = &s
	}
	var completedAt *string
	if p.CompletedAt != nil && !p.CompletedAt.IsZero() {
		s := p.CompletedAt.UTC().Format(time.RFC3339)
		completedAt = &s
	}
	return warmupPlanJSON{
		ID:                p.ID,
		Name:              p.Name,
		IPPool:            p.IPPool,
		TargetDailyVolume: p.TargetDailyVolume,
		CurrentDay:        p.CurrentDay,
		Stage:             p.Stage,
		StartedAt:         startedAt,
		CompletedAt:       completedAt,
		CreatedAt:         p.CreatedAt.UTC().Format(time.RFC3339),
	}
}

func formatWarmupDayStat(d pgstore.WarmupDayStat) warmupDayStatJSON {
	return warmupDayStatJSON{
		DayNumber:      d.DayNumber,
		StatDate:       d.StatDate.Format("2006-01-02"),
		Sent:           d.Sent,
		Delivered:      d.Delivered,
		Bounced:        d.Bounced,
		Deferred:       d.Deferred,
		AcceptanceRate: d.AcceptanceRate,
	}
}

// ---- handlers --------------------------------------------------------------

// GET /v1/warmup
func (s *Server) handleListWarmupPlans(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	plans, err := s.pg.ListWarmupPlans(r.Context(), tenant)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to list warm-up plans")
		return
	}
	out := make([]warmupPlanJSON, 0, len(plans))
	for _, p := range plans {
		out = append(out, formatWarmupPlan(p))
	}
	writeJSON(w, http.StatusOK, out)
}

// POST /v1/warmup
func (s *Server) handleCreateWarmupPlan(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	var req struct {
		Name              string `json:"name"`
		IPPool            string `json:"ip_pool"`
		TargetDailyVolume int    `json:"target_daily_volume"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "request body must be valid JSON")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "name is required")
		return
	}
	if req.TargetDailyVolume <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_field", "target_daily_volume must be a positive integer")
		return
	}
	id, err := s.pg.CreateWarmupPlan(r.Context(), tenant, req.Name, req.IPPool, req.TargetDailyVolume)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to create warm-up plan")
		return
	}
	plan, err := s.pg.GetWarmupPlan(r.Context(), tenant, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "plan created but could not be retrieved")
		return
	}
	writeJSON(w, http.StatusCreated, formatWarmupPlan(plan))
}

// GET /v1/warmup/{id}
func (s *Server) handleGetWarmupPlan(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	id := r.PathValue("id")
	plan, err := s.pg.GetWarmupPlan(r.Context(), tenant, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "warm-up plan not found")
		return
	}
	rawStats, err := s.pg.ListWarmupDayStats(r.Context(), plan.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to load day stats")
		return
	}
	stats := make([]warmupDayStatJSON, 0, len(rawStats))
	for _, d := range rawStats {
		stats = append(stats, formatWarmupDayStat(d))
	}
	detail := warmupPlanDetailJSON{
		warmupPlanJSON:   formatWarmupPlan(plan),
		DayStats:         stats,
		RecommendedToday: warmupRecommendedVolume(plan.CurrentDay, plan.TargetDailyVolume),
	}
	writeJSON(w, http.StatusOK, detail)
}

// PATCH /v1/warmup/{id}
func (s *Server) handleUpdateWarmupPlan(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	id := r.PathValue("id")
	var req struct {
		Stage string `json:"stage"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "request body must be valid JSON")
		return
	}
	switch req.Stage {
	case "ramping", "established", "paused":
		// valid
	default:
		writeError(w, http.StatusBadRequest, "invalid_field", "stage must be one of: ramping, established, paused")
		return
	}
	found, err := s.pg.UpdateWarmupStage(r.Context(), tenant, id, req.Stage)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to update warm-up plan")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "warm-up plan not found")
		return
	}
	plan, err := s.pg.GetWarmupPlan(r.Context(), tenant, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "plan updated but could not be retrieved")
		return
	}
	writeJSON(w, http.StatusOK, formatWarmupPlan(plan))
}

// DELETE /v1/warmup/{id}
func (s *Server) handleDeleteWarmupPlan(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	id := r.PathValue("id")
	found, err := s.pg.DeleteWarmupPlan(r.Context(), tenant, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to delete warm-up plan")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "warm-up plan not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
