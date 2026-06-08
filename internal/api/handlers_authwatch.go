package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/zezekim/mxsentinel/internal/authwatch"
)

// handleAuthSecurity (GET /v1/auth-security, ScopeRead) lists SASL credentials that
// authwatchd has flagged or that carry a lock record, with their recent behavioral signals
// and current lock state. Powers the Auth Security dashboard.
//
// NOTE: credential signals are keyed by sasl_username, which is global (not tenant-scoped) —
// the same as smtp_users (Dovecot looks logins up across all tenants). The endpoint requires
// ScopeRead; on a shared relay there is typically one operator tenant.
func (s *Server) handleAuthSecurity(w http.ResponseWriter, r *http.Request) {
	limit := parseIntParam(r, "signals", 10, 100)
	creds, err := authwatch.NewStore(s.pg.Pool).ListCredentials(r.Context(), limit)
	if err != nil {
		s.log.Error("list auth-security credentials", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to list credentials")
		return
	}

	type signalJSON struct {
		Signal     string          `json:"signal"`
		Detail     json.RawMessage `json:"detail"`
		DetectedAt string          `json:"detected_at"`
	}
	type credJSON struct {
		SASLUsername  string       `json:"sasl_username"`
		RecentSignals []signalJSON `json:"recent_signals"`
		Locked        bool         `json:"locked"`
		Reason        string       `json:"reason,omitempty"`
		LockedAt      *string      `json:"locked_at"`
	}

	items := make([]credJSON, 0, len(creds))
	for _, c := range creds {
		sigs := make([]signalJSON, 0, len(c.RecentSignals))
		for _, sig := range c.RecentSignals {
			sigs = append(sigs, signalJSON{
				Signal: sig.Signal, Detail: sig.Detail,
				DetectedAt: sig.DetectedAt.UTC().Format(time.RFC3339),
			})
		}
		var lockedAt *string
		if c.LockedAt != nil {
			ts := c.LockedAt.UTC().Format(time.RFC3339)
			lockedAt = &ts
		}
		items = append(items, credJSON{
			SASLUsername: c.SASLUsername, RecentSignals: sigs,
			Locked: c.Locked, Reason: c.Reason, LockedAt: lockedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"credentials": items})
}

type lockCredentialRequest struct {
	Locked bool   `json:"locked"`
	Reason string `json:"reason"`
}

// handleLockCredential (POST /v1/auth-security/{user}/lock, ScopeAdmin) sets the lock state
// for a SASL credential.
//
//   - locked=true  -> disables the SMTP user (Dovecot stops accepting its logins) and records
//     the lock. On a shared relay this is DRASTIC — it blocks every client that authenticates
//     with this credential.
//   - locked=false -> clears the lock record only. There is no re-enable-by-username store
//     method (DisableSMTPUserByUsername is disable-only), so the Dovecot login is re-enabled
//     separately from the SMTP Users page (Enable button). The response carries
//     reenable_via_smtp_users=true to surface this in the UI.
func (s *Server) handleLockCredential(w http.ResponseWriter, r *http.Request) {
	user := r.PathValue("user")
	if user == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "missing credential username")
		return
	}
	var req lockCredentialRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	store := authwatch.NewStore(s.pg.Pool)
	resp := map[string]any{"sasl_username": user, "locked": req.Locked}

	if req.Locked {
		if _, _, err := s.pg.DisableSMTPUserByUsername(r.Context(), user); err != nil {
			s.log.Error("disable smtp user (lock)", "user", user, "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "failed to lock credential")
			return
		}
	} else {
		// Disable is one-way at the username layer; tell the caller where to re-enable.
		resp["reenable_via_smtp_users"] = true
	}

	if err := store.SetLock(r.Context(), user, req.Locked, req.Reason); err != nil {
		s.log.Error("set credential lock", "user", user, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to record lock state")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}
