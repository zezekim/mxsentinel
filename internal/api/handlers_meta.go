package api

import (
	"net/http"
	"time"
)

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
	out := map[string]any{
		"tenant_id": a.TenantID,
		"scopes":    scopes,
		"user_id":   a.UserID,
		"role":      a.Role,
		// Lets a long-lived client discover its own identity and lifetime — the cPanel
		// plugin uses expires_at to decide when to renew itself.
		"credential_name": a.CredName,
	}
	// Absent rather than null when the credential never expires, so a client can tell
	// "nothing to renew" from "expiry unknown".
	if a.CredExpiresAt != nil {
		out["expires_at"] = a.CredExpiresAt.UTC().Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, out)
}
