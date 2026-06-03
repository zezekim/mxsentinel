package api

import (
	"encoding/json"
	"net/http"

	"github.com/zezekim/mxsentinel/internal/auth"
)

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// handleChangePassword lets the logged-in user change their own password. It is only
// available to session (user-login) callers — API tokens have no associated user — and
// requires the current password. requireAuth has already run (this route is on the authed
// mux), so the caller is authenticated; we do not require a particular scope because every
// role may change its own password.
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	a, ok := authFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	if a.UserID == "" {
		writeError(w, http.StatusForbidden, "forbidden", "only logged-in users can change a password (not API tokens)")
		return
	}
	var req changePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if len(req.NewPassword) < 8 {
		writeError(w, http.StatusBadRequest, "bad_request", "new password must be at least 8 characters")
		return
	}
	u, found, err := s.pg.GetUserByID(r.Context(), a.UserID)
	if err != nil {
		s.log.Error("get user", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not load account")
		return
	}
	if !found || u.PasswordHash == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "account has no password set")
		return
	}
	if !auth.CheckPassword(u.PasswordHash, req.CurrentPassword) {
		writeError(w, http.StatusForbidden, "forbidden", "current password is incorrect")
		return
	}
	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not hash password")
		return
	}
	if _, err := s.pg.UpdateUserPassword(r.Context(), a.UserID, a.TenantID, hash); err != nil {
		s.log.Error("update password", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not update password")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
