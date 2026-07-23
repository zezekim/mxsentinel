package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/zezekim/mxsentinel/internal/alertchannels"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
)

// registerAlertChannelRoutes wires the alert-channel endpoints into mux. The orchestrator
// calls this from server.go.
//
//	GET    /v1/alert-channels          list channels (secrets redacted)
//	POST   /v1/alert-channels          create a channel
//	PATCH  /v1/alert-channels/{id}     update name/enabled/config
//	DELETE /v1/alert-channels/{id}     delete a channel
//	POST   /v1/alert-channels/{id}/test  send a test notification
func (s *Server) registerAlertChannelRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/alert-channels", s.requireScope(ScopeRead, s.handleListAlertChannels))
	mux.HandleFunc("POST /v1/alert-channels", s.requireScope(ScopeWrite, s.handleCreateAlertChannel))
	mux.HandleFunc("PATCH /v1/alert-channels/{id}", s.requireScope(ScopeWrite, s.handleUpdateAlertChannel))
	mux.HandleFunc("DELETE /v1/alert-channels/{id}", s.requireScope(ScopeAdmin, s.handleDeleteAlertChannel))
	mux.HandleFunc("POST /v1/alert-channels/{id}/test", s.requireScope(ScopeWrite, s.handleTestAlertChannel))
}

// ---- response shapes -------------------------------------------------------

type alertChannelJSON struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Name      string          `json:"name"`
	Config    json.RawMessage `json:"config"` // secrets redacted to "***"
	Enabled   bool            `json:"enabled"`
	CreatedAt string          `json:"created_at"`
	UpdatedAt string          `json:"updated_at"`
}

func toAlertChannelJSON(c pgstore.AlertChannel) alertChannelJSON {
	redacted, err := alertchannels.RedactConfig(c.Type, c.Config)
	if err != nil || redacted == nil {
		redacted = []byte("{}")
	}
	return alertChannelJSON{
		ID:        c.ID,
		Type:      c.Type,
		Name:      c.Name,
		Config:    json.RawMessage(redacted),
		Enabled:   c.Enabled,
		CreatedAt: c.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: c.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// ---- handlers --------------------------------------------------------------

// GET /v1/alert-channels
func (s *Server) handleListAlertChannels(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	channels, err := s.pg.ListAlertChannels(r.Context(), tenant)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	out := make([]alertChannelJSON, len(channels))
	for i, c := range channels {
		out[i] = toAlertChannelJSON(c)
	}
	writeJSON(w, http.StatusOK, map[string]any{"alert_channels": out})
}

// POST /v1/alert-channels
func (s *Server) handleCreateAlertChannel(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)

	var body struct {
		Type   string          `json:"type"`
		Name   string          `json:"name"`
		Config json.RawMessage `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "request body must be valid JSON")
		return
	}
	if body.Name == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "name is required")
		return
	}
	if !alertchannels.ValidTypes[body.Type] {
		writeError(w, http.StatusBadRequest, "invalid_type", "type must be one of: slack, webhook, pagerduty, email")
		return
	}
	if len(body.Config) == 0 {
		body.Config = json.RawMessage("{}")
	}

	sealed, err := alertchannels.SealConfig(s.enc, body.Type, []byte(body.Config))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_config", err.Error())
		return
	}

	id, err := s.pg.CreateAlertChannel(r.Context(), tenant, body.Type, body.Name, sealed)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

// PATCH /v1/alert-channels/{id}
func (s *Server) handleUpdateAlertChannel(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	id := r.PathValue("id")

	existing, found, err := s.pg.GetAlertChannel(r.Context(), tenant, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "alert channel not found")
		return
	}

	// Defaults come from the existing row so a partial PATCH is well-defined.
	var body struct {
		Name    *string          `json:"name"`
		Enabled *bool            `json:"enabled"`
		Config  *json.RawMessage `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "request body must be valid JSON")
		return
	}

	name := existing.Name
	if body.Name != nil && *body.Name != "" {
		name = *body.Name
	}
	enabled := existing.Enabled
	if body.Enabled != nil {
		enabled = *body.Enabled
	}

	// Only re-seal config when the caller supplies a new one; otherwise leave it untouched.
	var sealed []byte
	if body.Config != nil {
		sealed, err = alertchannels.SealConfig(s.enc, existing.Type, []byte(*body.Config))
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_config", err.Error())
			return
		}
	}

	ok, err := s.pg.UpdateAlertChannel(r.Context(), tenant, id, name, enabled, sealed)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "alert channel not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// DELETE /v1/alert-channels/{id}
func (s *Server) handleDeleteAlertChannel(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	id := r.PathValue("id")

	found, err := s.pg.DeleteAlertChannel(r.Context(), tenant, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "alert channel not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// POST /v1/alert-channels/{id}/test
// Sends a synthetic notification through the channel's driver, bypassing dedup/throttle,
// and records the outcome in the delivery log. Never touches message content.
func (s *Server) handleTestAlertChannel(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	id := r.PathValue("id")

	ch, found, err := s.pg.GetAlertChannel(r.Context(), tenant, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "alert channel not found")
		return
	}

	// Decrypt secrets, decode config, build the driver, and send a test notification.
	plainCfg, err := alertchannels.OpenConfig(s.enc, ch.Type, ch.Config)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "config_error", "failed to read channel config")
		return
	}
	cfg, err := alertchannels.DecodeConfig(plainCfg)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "config_error", "failed to decode channel config")
		return
	}

	registry := alertchannels.NewRegistry(alertchannels.LoadConfig())
	notifier, ok := registry[ch.Type]
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_type", "no driver for channel type "+ch.Type)
		return
	}

	note := alertchannels.Notification{
		AlertRef:   "test:" + uuid.NewString(),
		Title:      "MX Sentinel test notification",
		Kind:       "test",
		Severity:   "info",
		Summary:    "This is a test notification from MX Sentinel. If you can read this, the channel works.",
		LinkURL:    alertchannels.LoadConfig().DashboardURL,
		OccurredAt: time.Now().UTC(),
		Test:       true,
	}

	sendErr := notifier.Send(r.Context(), note, cfg)
	status := alertchannels.StatusSent
	errStr := ""
	if sendErr != nil {
		status = alertchannels.StatusFailed
		errStr = sendErr.Error()
	}
	// Best-effort audit log (do not fail the request on a logging error).
	_ = s.pg.Record(r.Context(), ch.ID, note.AlertRef, status, errStr)

	if sendErr != nil {
		s.log.Warn("alert channel test failed", "tenant_id", tenant, "channel_id", ch.ID, "type", ch.Type, "err", sendErr)
		writeError(w, http.StatusBadGateway, "test_failed", "delivery failed: "+sendErr.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": status})
}
