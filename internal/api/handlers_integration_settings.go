package api

import (
	"encoding/json"
	"net/http"
	"strings"

	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
)

// Provider settings API (AI, Microsoft SNDS, Google Postmaster, notification email). Secret
// fields are write-only: accepted on PUT, never returned on GET (GET reports *_set booleans),
// and a blank secret on PUT preserves the stored one — same contract as the smarthost/cPanel
// credentials.

type aiSettingsJSON struct {
	Endpoint    string `json:"endpoint"`
	Model       string `json:"model"`
	APIKey      string `json:"api_key,omitempty"`
	APIKeySet   bool   `json:"api_key_set"`
	TimeoutSecs int    `json:"timeout_secs"`
}

type microsoftSettingsJSON struct {
	SNDSKey    string `json:"snds_key,omitempty"`
	SNDSKeySet bool   `json:"snds_key_set"`
}

type googleSettingsJSON struct {
	PostmasterToken    string `json:"postmaster_token,omitempty"`
	PostmasterTokenSet bool   `json:"postmaster_token_set"`
}

type notifyEmailSettingsJSON struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Username    string `json:"username"`
	Password    string `json:"password,omitempty"`
	PasswordSet bool   `json:"password_set"`
	From        string `json:"from"`
}

type integrationSettingsJSON struct {
	AI          aiSettingsJSON          `json:"ai"`
	Microsoft   microsoftSettingsJSON   `json:"microsoft"`
	Google      googleSettingsJSON      `json:"google"`
	NotifyEmail notifyEmailSettingsJSON `json:"notify_email"`
}

func toIntegrationSettingsJSON(m pgstore.IntegrationSettings) integrationSettingsJSON {
	return integrationSettingsJSON{
		AI: aiSettingsJSON{
			Endpoint:    m.AI.Endpoint,
			Model:       m.AI.Model,
			APIKeySet:   m.AI.APIKeyEnc != "",
			TimeoutSecs: m.AI.TimeoutSecs,
		},
		Microsoft: microsoftSettingsJSON{SNDSKeySet: m.Microsoft.SNDSKeyEnc != ""},
		Google:    googleSettingsJSON{PostmasterTokenSet: m.Google.PostmasterTokenEnc != ""},
		NotifyEmail: notifyEmailSettingsJSON{
			Host:        m.NotifyEmail.Host,
			Port:        m.NotifyEmail.Port,
			Username:    m.NotifyEmail.Username,
			PasswordSet: m.NotifyEmail.PasswordEnc != "",
			From:        m.NotifyEmail.From,
		},
	}
}

// GET /v1/settings/integrations — read scope.
func (s *Server) handleGetIntegrationSettings(w http.ResponseWriter, r *http.Request) {
	m, err := s.pg.GetIntegrationSettings(r.Context(), s.tenant(r))
	if err != nil {
		s.log.Error("get integration settings", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to read provider settings")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"integrations": toIntegrationSettingsJSON(m)})
}

// PUT /v1/settings/integrations — admin scope.
func (s *Server) handleUpdateIntegrationSettings(w http.ResponseWriter, r *http.Request) {
	var req integrationSettingsJSON
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	existing, err := s.pg.GetIntegrationSettings(r.Context(), s.tenant(r))
	if err != nil {
		s.log.Error("get integration settings (pre-update)", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to read provider settings")
		return
	}

	if req.NotifyEmail.Port != 0 && (req.NotifyEmail.Port < 1 || req.NotifyEmail.Port > 65535) {
		writeError(w, http.StatusBadRequest, "bad_request", "notify_email.port must be between 1 and 65535")
		return
	}
	if req.AI.TimeoutSecs != 0 && (req.AI.TimeoutSecs < 1 || req.AI.TimeoutSecs > 600) {
		writeError(w, http.StatusBadRequest, "bad_request", "ai.timeout_secs must be between 1 and 600")
		return
	}

	// seal preserves the existing sealed value when the plaintext input is blank.
	seal := func(plaintext, existingEnc string) (string, error) {
		if strings.TrimSpace(plaintext) == "" {
			return existingEnc, nil
		}
		return s.encSeal(plaintext)
	}

	out := pgstore.IntegrationSettings{
		AI: pgstore.AISettings{
			Endpoint:    strings.TrimSpace(req.AI.Endpoint),
			Model:       strings.TrimSpace(req.AI.Model),
			TimeoutSecs: req.AI.TimeoutSecs,
		},
		NotifyEmail: pgstore.NotifyEmailSettings{
			Host:     strings.TrimSpace(req.NotifyEmail.Host),
			Port:     req.NotifyEmail.Port,
			Username: strings.TrimSpace(req.NotifyEmail.Username),
			From:     strings.TrimSpace(req.NotifyEmail.From),
		},
	}
	if out.AI.APIKeyEnc, err = seal(req.AI.APIKey, existing.AI.APIKeyEnc); err != nil {
		s.sealError(w, err)
		return
	}
	if out.Microsoft.SNDSKeyEnc, err = seal(req.Microsoft.SNDSKey, existing.Microsoft.SNDSKeyEnc); err != nil {
		s.sealError(w, err)
		return
	}
	if out.Google.PostmasterTokenEnc, err = seal(req.Google.PostmasterToken, existing.Google.PostmasterTokenEnc); err != nil {
		s.sealError(w, err)
		return
	}
	if out.NotifyEmail.PasswordEnc, err = seal(req.NotifyEmail.Password, existing.NotifyEmail.PasswordEnc); err != nil {
		s.sealError(w, err)
		return
	}

	found, err := s.pg.UpdateIntegrationSettings(r.Context(), s.tenant(r), out)
	if err != nil {
		s.log.Error("update integration settings", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to save provider settings")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "tenant not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"integrations": toIntegrationSettingsJSON(out)})
}

func (s *Server) sealError(w http.ResponseWriter, err error) {
	s.log.Error("seal provider secret", "err", err)
	writeError(w, http.StatusInternalServerError, "internal", "failed to secure a credential")
}
