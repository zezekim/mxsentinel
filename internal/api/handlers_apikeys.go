package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// provisionedKeyTTL is the lifetime forced onto credentials minted by a provision-scoped
// caller. Unattended enrollment hands a secret to a machine we may never touch again, so it
// expires on its own; re-running the installer re-enrolls and gets a fresh year.
const provisionedKeyTTL = 365 * 24 * time.Hour

// provisionedNamePattern constrains the names a provision-scoped caller may claim. Pinning
// them to a cpanel- prefix means a leaked enrollment token cannot overwrite the operator's
// own credentials by minting something called "dashboard" and tripping the reissue path.
var provisionedNamePattern = regexp.MustCompile(`^cpanel-[a-z0-9][a-z0-9.-]*$`)

// mintDecision is what the escalation guard concluded about a mint request.
type mintDecision struct {
	isAdmin   bool       // caller literally holds admin, so nothing is constrained
	expiresAt *time.Time // nil = never expires (only reachable by an admin caller)
}

// mintError carries the HTTP shape of a refused mint.
type mintError struct {
	status  int
	code    string
	message string
}

func (e *mintError) Error() string { return e.message }

// authorizeMint is the escalation guard, kept pure so its every branch is unit-testable —
// it is the one piece of this feature where a mistake hands out a tenant-wide master key.
//
// It uses hasLiteralScope rather than hasScope deliberately: hasScope treats admin as a
// wildcard, so asking "does this caller hold provision?" through it would answer yes for
// admins and collapse the two branches into one.
func authorizeMint(callerScopes []string, name string, scopes []string, expiresIn string, now time.Time) (mintDecision, *mintError) {
	if hasLiteralScope(callerScopes, ScopeAdmin) {
		if expiresIn == "" {
			return mintDecision{isAdmin: true}, nil
		}
		d, err := time.ParseDuration(expiresIn)
		if err != nil || d <= 0 {
			return mintDecision{}, &mintError{http.StatusBadRequest, "bad_request",
				"expires_in must be a positive Go duration (e.g. \"8760h\")"}
		}
		t := now.Add(d).UTC()
		return mintDecision{isAdmin: true, expiresAt: &t}, nil
	}

	// Non-admin: the caller may only hand out privileges strictly weaker than a tenant
	// admin's, or the enrollment secret we ship to every new server becomes a master key.
	for _, sc := range scopes {
		if !provisionableScopes[sc] {
			return mintDecision{}, &mintError{http.StatusForbidden, "forbidden",
				"a provision-scoped token may only grant read and relay; requested: " + sc}
		}
	}
	if !provisionedNamePattern.MatchString(name) {
		return mintDecision{}, &mintError{http.StatusForbidden, "forbidden",
			"a provision-scoped token may only name keys cpanel-<host> (lowercase letters, digits, dot, hyphen)"}
	}
	// expiresIn from a provision caller is ignored rather than rejected: the installer has no
	// business choosing its own lifetime, and failing the enrollment over it helps nobody.
	t := now.Add(provisionedKeyTTL).UTC()
	return mintDecision{isAdmin: false, expiresAt: &t}, nil
}

type createAPIKeyRequest struct {
	Name      string   `json:"name"`
	Scopes    []string `json:"scopes"`
	ExpiresIn string   `json:"expires_in"` // Go duration string; ignored for provision callers
}

type apiKeyJSON struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Prefix    string   `json:"prefix"`
	Scopes    []string `json:"scopes"`
	CreatedAt string   `json:"created_at,omitempty"`
	ExpiresAt string   `json:"expires_at,omitempty"`
	RevokedAt string   `json:"revoked_at,omitempty"`
	LastUsed  string   `json:"last_used_at,omitempty"`
}

// handleCreateAPIKey mints an API credential and returns the secret exactly once.
//
// Two very different callers reach this handler. An admin token may mint anything — it could
// already do everything those credentials can do. A provision-scoped token may not: a
// credential that can mint admin credentials IS an admin credential, so allowing that would
// turn the narrow enrollment secret we ship on every new server into a tenant-wide master
// key. Hence the guard below, which is the entire security value of this endpoint.
func (s *Server) handleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	a, ok := authFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}

	var req createAPIKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "name is required")
		return
	}
	if len(req.Scopes) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "scopes is required (e.g. [\"read\",\"relay\"])")
		return
	}
	scopes := make([]string, 0, len(req.Scopes))
	for _, sc := range req.Scopes {
		sc = strings.ToLower(strings.TrimSpace(sc))
		if !knownScopes[sc] {
			writeError(w, http.StatusBadRequest, "bad_request", "unknown scope: "+sc)
			return
		}
		scopes = append(scopes, sc)
	}

	decision, mintErr := authorizeMint(a.Scopes, req.Name, scopes, req.ExpiresIn, time.Now())
	if mintErr != nil {
		writeError(w, mintErr.status, mintErr.code, mintErr.message)
		return
	}
	isAdmin, expiresAt := decision.isAdmin, decision.expiresAt

	token, prefix, hash, err := GenerateToken()
	if err != nil {
		s.log.Error("generate api token", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not generate token")
		return
	}

	tenant := s.tenant(r)
	var id string
	var replaced bool
	if isAdmin {
		// Admins get a plain insert: silently rotating a credential they didn't ask to
		// rotate would be a surprising way to lose access, so a collision is an error.
		id, err = s.pg.CreateAPICredential(r.Context(), tenant, req.Name, prefix, hash, scopes, expiresAt)
		if err != nil {
			if isUniqueViolation(err) {
				writeError(w, http.StatusConflict, "conflict", "a credential named "+req.Name+" already exists in this tenant")
				return
			}
			s.log.Error("create api credential", "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "could not create api credential")
			return
		}
	} else {
		// Enrollment must be idempotent: a rebuilt server re-runs the installer with the
		// same name and has to come back with a working key, not a 409.
		id, replaced, err = s.pg.ReissueAPICredential(r.Context(), tenant, req.Name, prefix, hash, scopes, expiresAt, provisionableScopeList)
		if err != nil {
			if isUniqueViolation(err) {
				// The name is taken by a credential this caller is not entitled to displace
				// (it holds scopes beyond read/relay). Refuse rather than revoke it.
				writeError(w, http.StatusConflict, "conflict",
					"a credential named "+req.Name+" already exists with broader scopes; revoke it deliberately before re-enrolling")
				return
			}
			s.log.Error("reissue api credential", "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "could not issue api credential")
			return
		}
	}

	// Deliberately logged: an enrollment is a security-relevant event, and this is the only
	// record of which credential was issued to whom. The token itself is never logged.
	s.log.Info("api credential issued",
		"id", id, "name", req.Name, "scopes", scopes,
		"by_cred", a.CredID, "admin", isAdmin, "replaced_existing", replaced)

	resp := apiKeyJSON{ID: id, Name: req.Name, Prefix: prefix, Scopes: scopes}
	if expiresAt != nil {
		resp.ExpiresAt = expiresAt.Format(time.RFC3339)
	}
	// The one and only time the secret leaves the server.
	writeJSON(w, http.StatusCreated, struct {
		apiKeyJSON
		Token string `json:"token"`
	}{apiKeyJSON: resp, Token: token})
}

// handleListAPIKeys lists the tenant's credentials without their secrets (admin scope).
func (s *Server) handleListAPIKeys(w http.ResponseWriter, r *http.Request) {
	creds, err := s.pg.ListAPICredentials(r.Context(), s.tenant(r))
	if err != nil {
		s.log.Error("list api credentials", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to list api credentials")
		return
	}
	items := make([]apiKeyJSON, 0, len(creds))
	for _, c := range creds {
		items = append(items, apiKeyJSON{
			ID: c.ID, Name: c.Name, Prefix: c.TokenPrefix, Scopes: c.Scopes,
			CreatedAt: c.CreatedAt, ExpiresAt: c.ExpiresAt, RevokedAt: c.RevokedAt, LastUsed: c.LastUsedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": items})
}

// handleRevokeAPIKey revokes a credential by id (admin scope). Revocation takes effect on the
// next request: auth filters on revoked_at at lookup time.
func (s *Server) handleRevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "id is required")
		return
	}
	found, err := s.pg.RevokeAPICredentialByID(r.Context(), s.tenant(r), id)
	if err != nil {
		s.log.Error("revoke api credential", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not revoke api credential")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "no such api credential in this tenant")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "revoked": true})
}

// isUniqueViolation reports whether err is a Postgres unique-constraint failure (SQLSTATE
// 23505), so a name collision can be answered with 409 rather than a blanket 500.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
