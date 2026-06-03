package api

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"

	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
)

// Defaults applied for unset mail settings, so the dashboard and generated guidance show
// sensible values out of the box.
const (
	defaultDKIMSelector    = "mxs"
	defaultDMARCPolicy     = "none"
	defaultRelayPort       = 587
	defaultResolverTimeout = 5
)

type mailSettingsJSON struct {
	SPFInclude   string `json:"spf_include"`
	DKIMSelector string `json:"dkim_selector"`
	DMARCPolicy  string `json:"dmarc_policy"`
	DMARCRua     string `json:"dmarc_rua"`
	DMARCRuf     string `json:"dmarc_ruf"`
	RelayHost    string `json:"relay_host"`
	RelayPort    int    `json:"relay_port"`

	ResolverAddress     string `json:"resolver_address"`
	ResolverTimeoutSecs int    `json:"resolver_timeout_secs"`
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
	if out.ResolverTimeoutSecs == 0 {
		out.ResolverTimeoutSecs = defaultResolverTimeout
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

// validResolverAddress accepts "" (system resolver), a bare IP/host, or host:port. It
// rejects whitespace and obviously malformed values; bad DNS servers still surface as
// lookup errors at recheck time.
func validResolverAddress(addr string) bool {
	if addr == "" {
		return true
	}
	if strings.ContainsAny(addr, " \t\r\n") {
		return false
	}
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	if host == "" {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	// Otherwise treat it as a hostname: at least one dot and no path separators.
	return !strings.ContainsAny(host, "/\\") && strings.Contains(host, ".")
}

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
	req.ResolverAddress = strings.TrimSpace(req.ResolverAddress)

	if req.DMARCPolicy != "" && !validDMARCPolicies[req.DMARCPolicy] {
		writeError(w, http.StatusBadRequest, "bad_request", "dmarc_policy must be none, quarantine, or reject")
		return
	}
	if req.RelayPort != 0 && (req.RelayPort < 1 || req.RelayPort > 65535) {
		writeError(w, http.StatusBadRequest, "bad_request", "relay_port must be between 1 and 65535")
		return
	}
	if !validResolverAddress(req.ResolverAddress) {
		writeError(w, http.StatusBadRequest, "bad_request", "resolver_address must be an IP or host, optionally with :port (e.g. 1.1.1.1 or 9.9.9.9:53)")
		return
	}
	if req.ResolverTimeoutSecs != 0 && (req.ResolverTimeoutSecs < 1 || req.ResolverTimeoutSecs > 60) {
		writeError(w, http.StatusBadRequest, "bad_request", "resolver_timeout_secs must be between 1 and 60")
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
