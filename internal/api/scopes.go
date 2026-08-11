package api

import "net/http"

// API token scopes (Phase 4 RBAC). admin is a superset of all scopes.
const (
	ScopeRead  = "read"  // GET endpoints
	ScopeWrite = "write" // mutating endpoints (recheck, resolve, ...)
	ScopeAdmin = "admin" // grants everything

	// ScopeRelay gates the /v1/smtp-users routes — provisioning the SASL credential a
	// relay client authenticates with. It exists so an on-server agent (the cPanel/WHM
	// plugin) can provision itself without holding a tenant-wide admin token. Because
	// admin satisfies every check, retargeting those routes from admin to relay is
	// backward-compatible: tokens already deployed in the field keep working.
	ScopeRelay = "relay"

	// ScopeProvision gates POST /v1/apikeys and nothing else. A credential that can mint
	// admin credentials is an admin credential, so the mint handler refuses to let a
	// provision-only caller grant scopes it does not itself hold (see mintableScopes).
	ScopeProvision = "provision"
)

// provisionableScopes are the only scopes a provision-scoped (non-admin) caller may grant.
// Keep this in sync with what an unattended relay client actually needs: reads for the
// status views, relay for SMTP-user provisioning. Adding admin or write here would defeat
// the point of the scope.
var provisionableScopes = map[string]bool{ScopeRead: true, ScopeRelay: true}

// provisionableScopeList is provisionableScopes as a slice, for the store's containment
// check on which existing credentials an enrollment may displace. Keep the two in sync.
var provisionableScopeList = []string{ScopeRead, ScopeRelay}

// knownScopes is the set accepted when minting. Unknown scopes are rejected rather than
// stored, so a typo can't silently produce a credential that satisfies nothing.
var knownScopes = map[string]bool{
	ScopeRead: true, ScopeWrite: true, ScopeRelay: true, ScopeProvision: true, ScopeAdmin: true,
}

// hasScope reports whether the granted scopes satisfy want (admin satisfies anything).
func hasScope(granted []string, want string) bool {
	for _, s := range granted {
		if s == want || s == ScopeAdmin {
			return true
		}
	}
	return false
}

// hasLiteralScope reports whether want appears verbatim in granted. Unlike hasScope it does
// NOT treat admin as a wildcard — the mint handler needs to distinguish "is actually admin"
// from "holds provision", because those two get very different minting privileges.
func hasLiteralScope(granted []string, want string) bool {
	for _, s := range granted {
		if s == want {
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
