package api

import (
	"encoding/json"
	"net/http"
	"strings"

	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
)

const (
	smarthostModeAlways     = "always"
	smarthostModeOnThrottle = "on_throttle"
	defaultSmarthostPort    = 587
)

// smarthostJSON is the API shape. The password is WRITE-ONLY: it is accepted on PUT but never
// returned on GET (GET reports password_set instead), matching how cPanel/WHMCS creds behave.
type smarthostJSON struct {
	Enabled     bool     `json:"enabled"`
	Host        string   `json:"host"`
	Port        int      `json:"port"`
	Username    string   `json:"username"`
	Password    string   `json:"password,omitempty"` // input only; blank on PUT keeps the stored one
	PasswordSet bool     `json:"password_set"`       // output only
	Mode        string   `json:"mode"`               // always | on_throttle
	Domains     []string `json:"domains"`
	TripRate    float64  `json:"trip_rate,omitempty"`
	WindowSecs  int      `json:"window_secs,omitempty"`
	HoldSecs    int      `json:"hold_secs,omitempty"`
	MinAttempts int      `json:"min_attempts,omitempty"`
	MinDefers   int      `json:"min_defers,omitempty"`
}

func toSmarthostJSON(m pgstore.SmarthostSettings) smarthostJSON {
	if m.Mode == "" {
		m.Mode = smarthostModeAlways
	}
	if m.Port == 0 {
		m.Port = defaultSmarthostPort
	}
	return smarthostJSON{
		Enabled:     m.Enabled,
		Host:        m.Host,
		Port:        m.Port,
		Username:    m.Username,
		PasswordSet: m.PasswordEnc != "",
		Mode:        m.Mode,
		Domains:     m.Domains,
		TripRate:    m.TripRate,
		WindowSecs:  m.WindowSecs,
		HoldSecs:    m.HoldSecs,
		MinAttempts: m.MinAttempts,
		MinDefers:   m.MinDefers,
	}
}

// GET /v1/settings/smarthost — read scope.
func (s *Server) handleGetSmarthost(w http.ResponseWriter, r *http.Request) {
	m, err := s.pg.GetSmarthostSettings(r.Context(), s.tenant(r))
	if err != nil {
		s.log.Error("get smarthost settings", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to read smarthost settings")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"smarthost": toSmarthostJSON(m)})
}

// PUT /v1/settings/smarthost — admin scope.
func (s *Server) handleUpdateSmarthost(w http.ResponseWriter, r *http.Request) {
	var req smarthostJSON
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	// Read existing so a blank password on PUT keeps the stored (sealed) one.
	existing, err := s.pg.GetSmarthostSettings(r.Context(), s.tenant(r))
	if err != nil {
		s.log.Error("get smarthost settings (pre-update)", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to read smarthost settings")
		return
	}

	req.Host = strings.TrimSpace(strings.ToLower(req.Host))
	req.Username = strings.TrimSpace(req.Username)
	req.Mode = strings.TrimSpace(strings.ToLower(req.Mode))
	if req.Mode == "" {
		req.Mode = smarthostModeAlways
	}
	if req.Port == 0 {
		req.Port = defaultSmarthostPort
	}
	domains := normalizeDomains(req.Domains)

	// Validation. Only enforce a full config when it's being enabled — you can save a draft
	// (disabled) with partial fields.
	if req.Mode != smarthostModeAlways && req.Mode != smarthostModeOnThrottle {
		writeError(w, http.StatusBadRequest, "bad_request", "mode must be 'always' or 'on_throttle'")
		return
	}
	if req.Port < 1 || req.Port > 65535 {
		writeError(w, http.StatusBadRequest, "bad_request", "port must be between 1 and 65535")
		return
	}
	if len(domains) > 100 {
		writeError(w, http.StatusBadRequest, "bad_request", "at most 100 domains")
		return
	}
	willHavePassword := req.Password != "" || existing.PasswordEnc != ""
	if req.Enabled {
		if req.Host == "" || req.Username == "" || !willHavePassword {
			writeError(w, http.StatusBadRequest, "bad_request", "host, username and password are required to enable the smarthost")
			return
		}
		if len(domains) == 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "add at least one recipient domain to route via the smarthost")
			return
		}
	}

	out := pgstore.SmarthostSettings{
		Enabled:     req.Enabled,
		Host:        req.Host,
		Port:        req.Port,
		Username:    req.Username,
		PasswordEnc: existing.PasswordEnc, // preserved unless a new password is supplied
		Mode:        req.Mode,
		Domains:     domains,
		TripRate:    req.TripRate,
		WindowSecs:  req.WindowSecs,
		HoldSecs:    req.HoldSecs,
		MinAttempts: req.MinAttempts,
		MinDefers:   req.MinDefers,
	}
	if req.Password != "" {
		sealed, err := s.encSeal(req.Password)
		if err != nil {
			s.log.Error("seal smarthost password", "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "failed to secure the password")
			return
		}
		out.PasswordEnc = sealed
	}

	found, err := s.pg.UpdateSmarthostSettings(r.Context(), s.tenant(r), out)
	if err != nil {
		s.log.Error("update smarthost settings", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to save smarthost settings")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "tenant not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"smarthost": toSmarthostJSON(out)})
}

// normalizeDomains lowercases, trims, de-dupes, and drops obviously invalid recipient-domain
// entries (anything with whitespace, a scheme, or a path).
func normalizeDomains(in []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, d := range in {
		d = strings.TrimSpace(strings.ToLower(d))
		d = strings.TrimPrefix(d, "@")
		if d == "" || strings.ContainsAny(d, " \t\r\n/\\:@") || !strings.Contains(d, ".") {
			continue
		}
		if _, ok := seen[d]; ok {
			continue
		}
		seen[d] = struct{}{}
		out = append(out, d)
	}
	return out
}
