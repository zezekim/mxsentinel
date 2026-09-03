package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/zezekim/mxsentinel/internal/auth"
)

// minSMTPPasswordLen is the floor for SMTP submission passwords. These are machine
// credentials embedded in smarthost configs, so we require a reasonable length.
const minSMTPPasswordLen = 8

type smtpUserJSON struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Domain   string `json:"domain"`
	Enabled  bool   `json:"enabled"`
	// WebmailAvailable reports whether this user can be opened in Roundcube with one click:
	// the feature must be configured AND the row must carry a sealed password copy. Users
	// created before the feature (or while no encryption key was set) have none until their
	// next password reset. See docs/webmail-autologin.md.
	WebmailAvailable bool   `json:"webmail_available"`
	CreatedAt        string `json:"created_at"`
}

// sealSMTPPassword returns the AES-256-GCM sealed copy of an SMTP password to store
// alongside its bcrypt hash, for Roundcube autologin. It returns "" — meaning "store NULL,
// webmail unavailable" — whenever no encryption key is configured, because Encryptor.Seal
// is a passthrough in that mode and would write the password to Postgres in the clear.
func (s *Server) sealSMTPPassword(password string) (string, error) {
	if !s.enc.Enabled() {
		return "", nil
	}
	return s.enc.Seal(password)
}

// handleListSMTPUsers lists the tenant's SMTP submission users (admin scope).
func (s *Server) handleListSMTPUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.pg.ListSMTPUsers(r.Context(), s.tenant(r))
	if err != nil {
		s.log.Error("list smtp users", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to list smtp users")
		return
	}
	items := make([]smtpUserJSON, 0, len(users))
	for _, u := range users {
		items = append(items, smtpUserJSON{
			ID: u.ID, Username: u.Username, Domain: u.Domain, Enabled: u.Enabled,
			WebmailAvailable: u.HasWebmail && s.webmailEnabled(), CreatedAt: u.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": items})
}

type createSMTPUserRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Domain   string `json:"domain"`
}

// handleCreateSMTPUser creates an SMTP submission credential in the caller's tenant
// (admin scope). The username must be globally unique — it is the SASL login the relay
// authenticates against.
func (s *Server) handleCreateSMTPUser(w http.ResponseWriter, r *http.Request) {
	var req createSMTPUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	req.Domain = strings.TrimSpace(req.Domain)
	if req.Username == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "username is required")
		return
	}
	if strings.ContainsAny(req.Username, " \t\r\n") {
		writeError(w, http.StatusBadRequest, "bad_request", "username must not contain whitespace")
		return
	}
	if len(req.Password) < minSMTPPasswordLen {
		writeError(w, http.StatusBadRequest, "bad_request", "password must be at least 8 characters")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not hash password")
		return
	}
	sealed, err := s.sealSMTPPassword(req.Password)
	if err != nil {
		s.log.Error("seal smtp password", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not store credential")
		return
	}
	id, err := s.pg.CreateSMTPUser(r.Context(), s.tenant(r), req.Username, hash, req.Domain, sealed)
	if err != nil {
		s.log.Error("create smtp user", "err", err)
		writeError(w, http.StatusConflict, "conflict", "could not create smtp user (username may already exist)")
		return
	}
	writeJSON(w, http.StatusCreated, smtpUserJSON{
		ID: id, Username: req.Username, Domain: req.Domain, Enabled: true,
		WebmailAvailable: sealed != "" && s.webmailEnabled(),
	})
}

type updateSMTPUserRequest struct {
	Enabled  *bool   `json:"enabled"`
	Password *string `json:"password"`
}

// handleUpdateSMTPUser toggles enabled and/or resets the password for an SMTP user
// (admin scope). Both fields are optional; at least one must be present.
func (s *Server) handleUpdateSMTPUser(w http.ResponseWriter, r *http.Request) {
	var req updateSMTPUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if req.Enabled == nil && req.Password == nil {
		writeError(w, http.StatusBadRequest, "bad_request", "provide 'enabled' and/or 'password'")
		return
	}
	tenant := s.tenant(r)
	id := r.PathValue("id")
	found := false

	if req.Password != nil {
		if len(*req.Password) < minSMTPPasswordLen {
			writeError(w, http.StatusBadRequest, "bad_request", "password must be at least 8 characters")
			return
		}
		hash, err := auth.HashPassword(*req.Password)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "could not hash password")
			return
		}
		sealed, err := s.sealSMTPPassword(*req.Password)
		if err != nil {
			s.log.Error("seal smtp password", "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "could not store credential")
			return
		}
		ok, err := s.pg.UpdateSMTPUserPassword(r.Context(), tenant, id, hash, sealed)
		if err != nil {
			s.log.Error("update smtp user password", "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "could not update smtp user")
			return
		}
		found = found || ok
	}
	if req.Enabled != nil {
		ok, err := s.pg.SetSMTPUserEnabled(r.Context(), tenant, id, *req.Enabled)
		if err != nil {
			s.log.Error("set smtp user enabled", "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "could not update smtp user")
			return
		}
		found = found || ok
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "smtp user not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleDeleteSMTPUser removes an SMTP submission credential (admin scope). Once deleted,
// the relay rejects new authentications for that username.
func (s *Server) handleDeleteSMTPUser(w http.ResponseWriter, r *http.Request) {
	found, err := s.pg.DeleteSMTPUser(r.Context(), s.tenant(r), r.PathValue("id"))
	if err != nil {
		s.log.Error("delete smtp user", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not delete smtp user")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "smtp user not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}
