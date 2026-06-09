package api

import (
	"encoding/json"
	"net/http"
	"time"

	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
)

// reportJSON is the wire shape for a report schedule.
type reportJSON struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	Frequency         string   `json:"frequency"`
	Recipients        []string `json:"recipients"`
	IncludeDNS        bool     `json:"include_dns"`
	IncludeDMARC      bool     `json:"include_dmarc"`
	IncludeIncidents  bool     `json:"include_incidents"`
	IncludeReputation bool     `json:"include_reputation"`
	Enabled           bool     `json:"enabled"`
	LastSentAt        *string  `json:"last_sent_at"`
	NextRunAt         *string  `json:"next_run_at"`
	CreatedAt         string   `json:"created_at"`
}

var validReportFrequencies = map[string]bool{
	"daily":   true,
	"weekly":  true,
	"monthly": true,
}

func toReportJSON(rs pgstore.ReportSchedule) reportJSON {
	j := reportJSON{
		ID:                rs.ID,
		Name:              rs.Name,
		Frequency:         rs.Frequency,
		Recipients:        rs.Recipients,
		IncludeDNS:        rs.IncludeDNS,
		IncludeDMARC:      rs.IncludeDMARC,
		IncludeIncidents:  rs.IncludeIncidents,
		IncludeReputation: rs.IncludeReputation,
		Enabled:           rs.Enabled,
		CreatedAt:         rs.CreatedAt.UTC().Format(time.RFC3339),
	}
	if rs.LastSentAt != nil {
		s := rs.LastSentAt.UTC().Format(time.RFC3339)
		j.LastSentAt = &s
	}
	if rs.NextRunAt != nil {
		s := rs.NextRunAt.UTC().Format(time.RFC3339)
		j.NextRunAt = &s
	}
	return j
}

// handleListReports handles GET /v1/reports — returns all report schedules for
// the authenticated tenant.
func (s *Server) handleListReports(w http.ResponseWriter, r *http.Request) {
	rows, err := s.pg.ListReportSchedules(r.Context(), s.tenant(r))
	if err != nil {
		s.log.Error("list report schedules", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to list report schedules")
		return
	}
	items := make([]reportJSON, 0, len(rows))
	for _, rs := range rows {
		items = append(items, toReportJSON(rs))
	}
	writeJSON(w, http.StatusOK, map[string]any{"reports": items, "count": len(items)})
}

// handleCreateReport handles POST /v1/reports — creates a new report schedule.
func (s *Server) handleCreateReport(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name              string   `json:"name"`
		Frequency         string   `json:"frequency"`
		Recipients        []string `json:"recipients"`
		IncludeDNS        bool     `json:"include_dns"`
		IncludeDMARC      bool     `json:"include_dmarc"`
		IncludeIncidents  bool     `json:"include_incidents"`
		IncludeReputation bool     `json:"include_reputation"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if body.Name == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "name is required")
		return
	}
	if !validReportFrequencies[body.Frequency] {
		writeError(w, http.StatusBadRequest, "bad_request", `frequency must be "daily", "weekly", or "monthly"`)
		return
	}
	if len(body.Recipients) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "at least one recipient is required")
		return
	}
	if len(body.Recipients) > 10 {
		writeError(w, http.StatusBadRequest, "bad_request", "maximum 10 recipients allowed")
		return
	}

	id, err := s.pg.CreateReportSchedule(
		r.Context(), s.tenant(r),
		body.Name, body.Frequency, body.Recipients,
		body.IncludeDNS, body.IncludeDMARC, body.IncludeIncidents, body.IncludeReputation,
	)
	if err != nil {
		s.log.Error("create report schedule", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to create report schedule")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

// handleUpdateReport handles PUT /v1/reports/{id} — updates an existing report schedule.
func (s *Server) handleUpdateReport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Frequency         string   `json:"frequency"`
		Recipients        []string `json:"recipients"`
		IncludeDNS        bool     `json:"include_dns"`
		IncludeDMARC      bool     `json:"include_dmarc"`
		IncludeIncidents  bool     `json:"include_incidents"`
		IncludeReputation bool     `json:"include_reputation"`
		Enabled           bool     `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if !validReportFrequencies[body.Frequency] {
		writeError(w, http.StatusBadRequest, "bad_request", `frequency must be "daily", "weekly", or "monthly"`)
		return
	}
	if len(body.Recipients) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "at least one recipient is required")
		return
	}
	if len(body.Recipients) > 10 {
		writeError(w, http.StatusBadRequest, "bad_request", "maximum 10 recipients allowed")
		return
	}

	found, err := s.pg.UpdateReportSchedule(
		r.Context(), s.tenant(r), id,
		body.Frequency, body.Recipients,
		body.IncludeDNS, body.IncludeDMARC, body.IncludeIncidents, body.IncludeReputation,
		body.Enabled,
	)
	if err != nil {
		s.log.Error("update report schedule", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to update report schedule")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "report schedule not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleDeleteReport handles DELETE /v1/reports/{id} — removes a report schedule.
func (s *Server) handleDeleteReport(w http.ResponseWriter, r *http.Request) {
	found, err := s.pg.DeleteReportSchedule(r.Context(), s.tenant(r), r.PathValue("id"))
	if err != nil {
		s.log.Error("delete report schedule", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to delete report schedule")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "report schedule not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

// handleSendReportNow handles POST /v1/reports/{id}/send-now — marks the
// schedule due immediately by setting next_run_at to now minus one second so
// that reportd picks it up on its next poll.
func (s *Server) handleSendReportNow(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tenantID := s.tenant(r)

	const q = `
		UPDATE report_schedules
		SET next_run_at = NOW() - INTERVAL '1 second',
		    updated_at  = NOW()
		WHERE tenant_id = $1 AND id = $2`

	tag, err := s.pg.Pool.Exec(r.Context(), q, tenantID, id)
	if err != nil {
		s.log.Error("send report now", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to trigger report")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "not_found", "report schedule not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"queued": true})
}
