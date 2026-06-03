package api

import (
	"encoding/json"
	"net/http"
	"time"
)

type incidentJSON struct {
	ID            string          `json:"id"`
	SourceEventID string          `json:"source_event_id"`
	Kind          string          `json:"kind"`
	Severity      string          `json:"severity"`
	Domain        string          `json:"domain"`
	Subject       string          `json:"subject"`
	Title         string          `json:"title"`
	Detail        json.RawMessage `json:"detail,omitempty"`
	Status        string          `json:"status"`
	Confidence    *float64        `json:"confidence"`
	CreatedAt     string          `json:"created_at"`
	ResolvedAt    *string         `json:"resolved_at"`

	// AI diagnostics (Phase 3); null until aid analyzes the incident.
	AISummary     *string         `json:"ai_summary"`
	AIRemediation json.RawMessage `json:"ai_remediation,omitempty"`
	AIModel       *string         `json:"ai_model"`
	AIAnalyzedAt  *string         `json:"ai_analyzed_at"`
}

// handleListIncidents lists a tenant's incidents (filterable by status/domain).
func (s *Server) handleListIncidents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rows, err := s.pg.ListIncidents(r.Context(), s.tenant(r), q.Get("status"), q.Get("domain"),
		parseIntParam(r, "limit", 50, 500))
	if err != nil {
		s.log.Error("list incidents", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to list incidents")
		return
	}
	items := make([]incidentJSON, 0, len(rows))
	for _, i := range rows {
		var resolved *string
		if i.ResolvedAt != nil {
			ts := i.ResolvedAt.UTC().Format(time.RFC3339)
			resolved = &ts
		}
		var analyzed *string
		if i.AIAnalyzedAt != nil {
			ts := i.AIAnalyzedAt.UTC().Format(time.RFC3339)
			analyzed = &ts
		}
		items = append(items, incidentJSON{
			ID: i.ID, SourceEventID: i.SourceEventID, Kind: i.Kind, Severity: i.Severity,
			Domain: i.Domain, Subject: i.Subject, Title: i.Title, Detail: i.Detail,
			Status: i.Status, Confidence: i.Confidence,
			CreatedAt: i.CreatedAt.UTC().Format(time.RFC3339), ResolvedAt: resolved,
			AISummary: i.AISummary, AIRemediation: i.AIRemediation, AIModel: i.AIModel, AIAnalyzedAt: analyzed,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"incidents": items})
}

// handleResolveIncident marks an incident resolved.
func (s *Server) handleResolveIncident(w http.ResponseWriter, r *http.Request) {
	found, err := s.pg.ResolveIncident(r.Context(), s.tenant(r), r.PathValue("id"))
	if err != nil {
		s.log.Error("resolve incident", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to resolve incident")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "incident not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"resolved": true})
}
