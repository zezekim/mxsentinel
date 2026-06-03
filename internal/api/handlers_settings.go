package api

import (
	"encoding/json"
	"net/http"
	"strings"

	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
)

// Defaults applied for unset mail settings, so the dashboard and generated guidance show
// sensible values out of the box.
const (
	defaultDKIMSelector = "mxs"
	defaultDMARCPolicy  = "none"
	defaultRelayPort    = 587
)

type mailSettingsJSON struct {
	SPFInclude   string `json:"spf_include"`
	DKIMSelector string `json:"dkim_selector"`
	DMARCPolicy  string `json:"dmarc_policy"`
	DMARCRua     string `json:"dmarc_rua"`
	DMARCRuf     string `json:"dmarc_ruf"`
	RelayHost    string `json:"relay_host"`
	RelayPort    int    `json:"relay_port"`
}

func toMailSettingsJSON(m pgstore.MailSettings) mailSettingsJSON {
	out := mailSettingsJSON(m)
	if out.DKIMSelector == "" {
		out.DKIMSelector = defaultDKIMSelector
	}
	if out.DMARCPolicy == "" {
		out.DMARCPolicy = defaultDMARCPolicy
	}
	if out.RelayPort == 0 {
		out.RelayPort = defaultRelayPort
	}
	return out
}

// handleGetSettings returns the tenant's mail settings (read scope).
func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	m, err := s.pg.GetMailSettings(r.Context(), s.tenant(r))
	if err != nil {
		s.log.Error("get settings", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to read settings")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": toMailSettingsJSON(m)})
}

var validDMARCPolicies = map[string]bool{"none": true, "quarantine": true, "reject": true}

// handleUpdateSettings replaces the tenant's mail settings (admin scope).
func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var req mailSettingsJSON
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	req.SPFInclude = strings.TrimSpace(req.SPFInclude)
	req.DKIMSelector = strings.TrimSpace(req.DKIMSelector)
	req.DMARCPolicy = strings.TrimSpace(strings.ToLower(req.DMARCPolicy))
	req.DMARCRua = strings.TrimSpace(req.DMARCRua)
	req.DMARCRuf = strings.TrimSpace(req.DMARCRuf)
	req.RelayHost = strings.TrimSpace(req.RelayHost)

	if req.DMARCPolicy != "" && !validDMARCPolicies[req.DMARCPolicy] {
		writeError(w, http.StatusBadRequest, "bad_request", "dmarc_policy must be none, quarantine, or reject")
		return
	}
	if req.RelayPort != 0 && (req.RelayPort < 1 || req.RelayPort > 65535) {
		writeError(w, http.StatusBadRequest, "bad_request", "relay_port must be between 1 and 65535")
		return
	}

	m := pgstore.MailSettings(req)
	found, err := s.pg.UpdateMailSettings(r.Context(), s.tenant(r), m)
	if err != nil {
		s.log.Error("update settings", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to save settings")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "tenant not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": toMailSettingsJSON(m)})
}
