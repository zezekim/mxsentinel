package api

import "net/http"

// API token scopes (Phase 4 RBAC). admin is a superset of all scopes.
const (
	ScopeRead  = "read"  // GET endpoints
	ScopeWrite = "write" // mutating endpoints (recheck, resolve, ...)
	ScopeAdmin = "admin" // grants everything
)

// hasScope reports whether the granted scopes satisfy want (admin satisfies anything).
func hasScope(granted []string, want string) bool {
	for _, s := range granted {
		if s == want || s == ScopeAdmin {
			return true
		}
	}
	return false
}

// requireScope wraps a handler so it only runs when the authenticated caller holds the
// required scope. requireAuth must have run first (it populates the auth context).
func (s *Server) requireScope(scope string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a, ok := authFromContext(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		if !hasScope(a.Scopes, scope) {
			writeError(w, http.StatusForbidden, "forbidden", "missing required scope: "+scope)
			return
		}
		h(w, r)
	}
}
