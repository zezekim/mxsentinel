package api

import (
	"encoding/json"
	"net/http"
	"time"

	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
)

// ---- response shapes -------------------------------------------------------

type alertRuleJSON struct {
	ID         string              `json:"id"`
	Name       string              `json:"name"`
	Signal     string              `json:"signal"`
	Condition  json.RawMessage     `json:"condition"`
	ChannelIDs []string            `json:"channel_ids"`
	Enabled    bool                `json:"enabled"`
	CreatedAt  string              `json:"created_at"`
	UpdatedAt  string              `json:"updated_at"`
}

type notificationChannelJSON struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	Name      string          `json:"name"`
	Config    json.RawMessage `json:"config"`
	Enabled   bool            `json:"enabled"`
	CreatedAt string          `json:"created_at"`
}

type alertEventJSON struct {
	ID          string          `json:"id"`
	RuleID      string          `json:"rule_id"`
	State       string          `json:"state"`
	TriggeredAt string          `json:"triggered_at"`
	ResolvedAt  *string         `json:"resolved_at"`
	Payload     json.RawMessage `json:"payload"`
	CreatedAt   string          `json:"created_at"`
}

// ---- helpers ---------------------------------------------------------------

func toAlertRuleJSON(r pgstore.AlertRule) alertRuleJSON {
	return alertRuleJSON{
		ID:         r.ID,
		Name:       r.Name,
		Signal:     r.Signal,
		Condition:  json.RawMessage(r.Condition),
		ChannelIDs: r.ChannelIDs,
		Enabled:    r.Enabled,
		CreatedAt:  r.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:  r.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func toNotificationChannelJSON(c pgstore.NotificationChannel) notificationChannelJSON {
	return notificationChannelJSON{
		ID:        c.ID,
		Kind:      c.Kind,
		Name:      c.Name,
		Config:    json.RawMessage(c.Config),
		Enabled:   c.Enabled,
		CreatedAt: c.CreatedAt.UTC().Format(time.RFC3339),
	}
}

func toAlertEventJSON(e pgstore.AlertEvent) alertEventJSON {
	j := alertEventJSON{
		ID:          e.ID,
		RuleID:      e.RuleID,
		State:       e.State,
		TriggeredAt: e.TriggeredAt.UTC().Format(time.RFC3339),
		Payload:     json.RawMessage(e.Payload),
		CreatedAt:   e.CreatedAt.UTC().Format(time.RFC3339),
	}
	if e.ResolvedAt != nil {
		s := e.ResolvedAt.UTC().Format(time.RFC3339)
		j.ResolvedAt = &s
	}
	return j
}

// ---- handlers --------------------------------------------------------------

// GET /v1/alert-rules
func (s *Server) handleListAlertRules(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	rules, err := s.pg.ListAlertRules(r.Context(), tenant)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	out := make([]alertRuleJSON, len(rules))
	for i, rule := range rules {
		out[i] = toAlertRuleJSON(rule)
	}
	writeJSON(w, http.StatusOK, map[string]any{"alert_rules": out})
}

// POST /v1/alert-rules
func (s *Server) handleCreateAlertRule(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)

	var body struct {
		Name       string          `json:"name"`
		Signal     string          `json:"signal"`
		Condition  json.RawMessage `json:"condition"`
		ChannelIDs []string        `json:"channel_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "request body must be valid JSON")
		return
	}
	if body.Name == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "name is required")
		return
	}
	if body.Signal == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "signal is required")
		return
	}
	if len(body.Condition) == 0 {
		body.Condition = json.RawMessage("{}")
	}
	if body.ChannelIDs == nil {
		body.ChannelIDs = []string{}
	}

	id, err := s.pg.CreateAlertRule(r.Context(), tenant, body.Name, body.Signal, []byte(body.Condition), body.ChannelIDs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

// PATCH /v1/alert-rules/{id}
func (s *Server) handleUpdateAlertRule(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	id := r.PathValue("id")

	var body struct {
		Enabled    bool            `json:"enabled"`
		Condition  json.RawMessage `json:"condition"`
		ChannelIDs []string        `json:"channel_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "request body must be valid JSON")
		return
	}
	if len(body.Condition) == 0 {
		body.Condition = json.RawMessage("{}")
	}
	if body.ChannelIDs == nil {
		body.ChannelIDs = []string{}
	}

	found, err := s.pg.UpdateAlertRule(r.Context(), tenant, id, body.Enabled, []byte(body.Condition), body.ChannelIDs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "alert rule not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// DELETE /v1/alert-rules/{id}
func (s *Server) handleDeleteAlertRule(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	id := r.PathValue("id")

	found, err := s.pg.DeleteAlertRule(r.Context(), tenant, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "alert rule not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// GET /v1/notification-channels
func (s *Server) handleListChannels(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	channels, err := s.pg.ListNotificationChannels(r.Context(), tenant)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	out := make([]notificationChannelJSON, len(channels))
	for i, ch := range channels {
		out[i] = toNotificationChannelJSON(ch)
	}
	writeJSON(w, http.StatusOK, map[string]any{"channels": out})
}

// POST /v1/notification-channels
func (s *Server) handleCreateChannel(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)

	var body struct {
		Kind   string          `json:"kind"`
		Name   string          `json:"name"`
		Config json.RawMessage `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "request body must be valid JSON")
		return
	}
	if body.Kind == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "kind is required")
		return
	}
	if body.Name == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "name is required")
		return
	}

	validKinds := map[string]bool{"email": true, "slack": true, "webhook": true, "pagerduty": true}
	if !validKinds[body.Kind] {
		writeError(w, http.StatusBadRequest, "invalid_kind", "kind must be one of: email, slack, webhook, pagerduty")
		return
	}
	if len(body.Config) == 0 {
		body.Config = json.RawMessage("{}")
	}

	id, err := s.pg.CreateNotificationChannel(r.Context(), tenant, body.Kind, body.Name, []byte(body.Config))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

// DELETE /v1/notification-channels/{id}
func (s *Server) handleDeleteChannel(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	id := r.PathValue("id")

	found, err := s.pg.DeleteNotificationChannel(r.Context(), tenant, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "notification channel not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// GET /v1/alert-events
func (s *Server) handleListAlertEvents(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	limit := parseIntParam(r, "limit", 50, 500)

	events, err := s.pg.ListAlertEvents(r.Context(), tenant, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	out := make([]alertEventJSON, len(events))
	for i, ev := range events {
		out[i] = toAlertEventJSON(ev)
	}
	writeJSON(w, http.StatusOK, map[string]any{"alert_events": out})
}
