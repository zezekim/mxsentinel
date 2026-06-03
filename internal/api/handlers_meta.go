package api

import "net/http"

// handleMe returns the authenticated caller's tenant and granted scopes — useful for
// clients to discover what they're allowed to do.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	a, ok := authFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	scopes := a.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tenant_id": a.TenantID,
		"scopes":    scopes,
		"user_id":   a.UserID,
		"role":      a.Role,
	})
}
