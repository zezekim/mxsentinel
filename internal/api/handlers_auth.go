package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/zezekim/mxsentinel/internal/auth"
)

const sessionTTL = 24 * time.Hour

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// handleLogin authenticates email+password and issues a session token. Public route.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil {
		writeError(w, http.StatusServiceUnavailable, "login_disabled", "user login is not enabled")
		return
	}
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	req.Email = strings.TrimSpace(req.Email)
	if req.Email == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "email and password are required")
		return
	}

	u, found, err := s.pg.GetUserByEmail(r.Context(), req.Email)
	if err != nil {
		s.log.Error("login lookup failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "login failed")
		return
	}
	// Generic error regardless of which check fails, to avoid user enumeration.
	if !found || u.Status == "disabled" || u.PasswordHash == "" || !auth.CheckPassword(u.PasswordHash, req.Password) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid credentials")
		return
	}

	token, err := s.sessions.Create(r.Context(),
		auth.Session{UserID: u.ID, TenantID: u.TenantID, Role: u.Role}, sessionTTL)
	if err != nil {
		s.log.Error("session create failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not start session")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token":      token,
		"expires_at": time.Now().Add(sessionTTL).UTC().Format(time.RFC3339),
		"user": map[string]any{
			"id": u.ID, "email": u.Email, "tenant_id": u.TenantID, "role": u.Role,
		},
	})
}

// handleLogout revokes the caller's session token. Authed route.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if token, ok := bearer(r.Header.Get("Authorization")); ok && s.sessions != nil {
		if err := s.sessions.Delete(r.Context(), token); err != nil {
			s.log.Warn("session delete failed", "err", err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
