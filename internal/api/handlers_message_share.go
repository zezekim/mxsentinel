package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	chstore "github.com/zezekim/mxsentinel/internal/store/clickhouse"
)

// ── Owner-facing (authenticated) share-link management ─────────────────────────
//
// A share link is a capability URL to one message's delivery trace. The message is
// identified by its relay-local queue id (stable even when a message has no Message-ID).
// See migrations/postgres/00016_message_share_links.sql for the security model.

type shareLinkJSON struct {
	ID           string  `json:"id"`
	QueueID      string  `json:"queue_id"`
	MessageID    string  `json:"message_id"`
	Label        string  `json:"label"`
	URL          string  `json:"url"`             // full shareable URL (public base + /trace/<token>)
	Path         string  `json:"path"`            // "/trace/<token>" — for clients that compose their own origin
	Token        string  `json:"token,omitempty"` // returned ONCE, only at creation
	Active       bool    `json:"active"`
	ViewCount    int     `json:"view_count"`
	ExpiresAt    *string `json:"expires_at"`
	RevokedAt    *string `json:"revoked_at"`
	LastViewedAt *string `json:"last_viewed_at"`
	CreatedAt    string  `json:"created_at"`
}

// tracePath builds the site-relative path a share token resolves to.
func tracePath(token string) string { return "/trace/" + token }

// traceURL prefixes tracePath with the configured public base, if any. When no base is
// configured it returns just the path and the frontend composes its own origin.
func (s *Server) traceURL(token string) string {
	if s.publicBaseURL == "" {
		return tracePath(token)
	}
	return s.publicBaseURL + tracePath(token)
}

// handleCreateShareLink mints a shareable trace link for a message the tenant owns.
//
// POST /v1/messages/{queueID}/share   (scope: write)
// body (optional): { "label": string, "ttl_hours": int }
func (s *Server) handleCreateShareLink(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	queueID := r.PathValue("queueID")
	if queueID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "queue id is required")
		return
	}

	var body struct {
		Label    string `json:"label"`
		TTLHours int    `json:"ttl_hours"`
	}
	// Body is optional; tolerate an empty request.
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid_body", "request body must be valid JSON")
		return
	}

	// Ownership check: the tenant must actually have telemetry for this queue id.
	trace, err := s.ch.QueryMessageTrace(r.Context(), tenant, queueID)
	if err != nil {
		s.log.Error("query message trace for share", "tenant_id", tenant, "queue_id", queueID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to look up message")
		return
	}
	if len(trace.Events) == 0 {
		writeError(w, http.StatusNotFound, "not_found", "no message found for that queue id")
		return
	}

	token, prefix, hash, err := GenerateShareToken()
	if err != nil {
		s.log.Error("generate share token", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to mint token")
		return
	}

	var expiresAt *time.Time
	if body.TTLHours > 0 {
		t := time.Now().Add(time.Duration(body.TTLHours) * time.Hour)
		expiresAt = &t
	}

	// created_by is set only for user-session auth (API tokens have no user id).
	a, _ := authFromContext(r.Context())

	id, err := s.pg.CreateShareLink(r.Context(), tenant, queueID, trace.MessageID, prefix, hash, body.Label, a.UserID, expiresAt)
	if err != nil {
		s.log.Error("create share link", "tenant_id", tenant, "queue_id", queueID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to create share link")
		return
	}

	out := shareLinkJSON{
		ID:        id,
		QueueID:   queueID,
		MessageID: trace.MessageID,
		Label:     body.Label,
		URL:       s.traceURL(token),
		Path:      tracePath(token),
		Token:     token, // shown once
		Active:    true,
		ViewCount: 0,
		ExpiresAt: rfc3339Ptr(expiresAt),
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	writeJSON(w, http.StatusCreated, out)
}

// handleListShareLinks lists the share links minted for a message.
//
// GET /v1/messages/{queueID}/shares   (scope: read)
func (s *Server) handleListShareLinks(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	queueID := r.PathValue("queueID")
	if queueID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "queue id is required")
		return
	}

	links, err := s.pg.ListShareLinks(r.Context(), tenant, queueID)
	if err != nil {
		s.log.Error("list share links", "tenant_id", tenant, "queue_id", queueID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to list share links")
		return
	}

	out := make([]shareLinkJSON, 0, len(links))
	for _, l := range links {
		// The plaintext token is never recoverable after creation; expose the path via prefix
		// is not possible, so list entries carry no clickable URL — only status/metadata.
		out = append(out, shareLinkJSON{
			ID:           l.ID,
			QueueID:      l.QueueID,
			MessageID:    l.MessageID,
			Label:        l.Label,
			Active:       l.Active(),
			ViewCount:    l.ViewCount,
			ExpiresAt:    rfc3339Ptr(l.ExpiresAt),
			RevokedAt:    rfc3339Ptr(l.RevokedAt),
			LastViewedAt: rfc3339Ptr(l.LastViewedAt),
			CreatedAt:    l.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"shares": out, "count": len(out)})
}

// handleRevokeShareLink revokes a share link so its URL stops resolving.
//
// DELETE /v1/messages/shares/{id}   (scope: write)
func (s *Server) handleRevokeShareLink(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "id is required")
		return
	}

	revoked, err := s.pg.RevokeShareLink(r.Context(), tenant, id)
	if err != nil {
		s.log.Error("revoke share link", "tenant_id", tenant, "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to revoke share link")
		return
	}
	if !revoked {
		writeError(w, http.StatusNotFound, "not_found", "share link not found or already revoked")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revoked": true})
}

// ── Public (unauthenticated) trace resolution ─────────────────────────────────

type traceEventJSON struct {
	EventTime       string `json:"event_time"`
	EventType       string `json:"event_type"`
	Provider        string `json:"provider"`
	MXHost          string `json:"mx_host"`
	RecipientDomain string `json:"recipient_domain"`
	SMTPCode        uint16 `json:"smtp_code"`
	EnhancedStatus  string `json:"enhanced_status"`
	BounceClass     string `json:"bounce_class"`
	ResponseText    string `json:"response_text"`
}

type publicTraceResponse struct {
	MessageID       string           `json:"message_id"`
	FromDomain      string           `json:"from_domain"`
	RecipientDomain string           `json:"recipient_domain"`
	Provider        string           `json:"provider"`
	Status          string           `json:"status"` // final outcome (latest event type)
	Label           string           `json:"label"`
	Events          []traceEventJSON `json:"events"`
	CheckedAt       string           `json:"checked_at"`
}

// handlePublicTrace resolves a share token to a message's delivery trace. No auth: the token
// IS the capability. The response deliberately omits internal identifiers (queue id, relay IP,
// SMTP username, tenant) — it exposes only what a delivery receipt should: sending domain,
// recipient domain, provider, the status ladder, and each provider response. Message bodies
// and full recipient addresses are never stored (privacy boundary), so they cannot leak here.
//
// GET /v1/trace/{token}
func (s *Server) handlePublicTrace(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	prefix := ShareTokenPrefixOf(token)
	if prefix == "" {
		writeError(w, http.StatusNotFound, "not_found", "invalid or unknown trace link")
		return
	}

	ctx := r.Context()
	link, found, err := s.pg.GetShareLinkByPrefix(ctx, prefix)
	if err != nil {
		s.log.Error("resolve share link", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to resolve link")
		return
	}
	// Uniform "not found" for unknown prefix or a wrong secret (constant-time compare) so the
	// endpoint reveals nothing to enumeration attempts.
	if !found || !tokenMatches(token, link.TokenHash) {
		writeError(w, http.StatusNotFound, "not_found", "invalid or unknown trace link")
		return
	}
	if link.RevokedAt != nil {
		writeError(w, http.StatusGone, "revoked", "this link has been revoked")
		return
	}
	if link.ExpiresAt != nil && link.ExpiresAt.Before(time.Now()) {
		writeError(w, http.StatusGone, "expired", "this link has expired")
		return
	}

	trace, err := s.ch.QueryMessageTrace(ctx, link.TenantID, link.QueueID)
	if err != nil {
		s.log.Error("query public trace", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to load message trace")
		return
	}
	if len(trace.Events) == 0 {
		// Telemetry aged out of the 90-day TTL, or was never written.
		writeError(w, http.StatusNotFound, "not_found", "no delivery data for this message (it may have aged out)")
		return
	}

	// Best-effort view accounting; never fail the response on a bump error.
	if terr := s.pg.TouchShareLink(ctx, link.ID); terr != nil {
		s.log.Warn("touch share link", "id", link.ID, "err", terr)
	}

	writeJSON(w, http.StatusOK, buildPublicTrace(link.Label, trace))
}

// buildPublicTrace shapes a ClickHouse trace into the public response (final status = the
// most recent event's type; recipient/provider taken from the last event that carries them).
func buildPublicTrace(label string, trace chstore.MessageTrace) publicTraceResponse {
	events := make([]traceEventJSON, 0, len(trace.Events))
	var status, provider, recipient string
	for _, e := range trace.Events {
		events = append(events, traceEventJSON{
			EventTime:       e.EventTime.UTC().Format(time.RFC3339),
			EventType:       e.EventType,
			Provider:        e.Provider,
			MXHost:          e.MXHost,
			RecipientDomain: e.RecipientDomain,
			SMTPCode:        e.SMTPCode,
			EnhancedStatus:  e.EnhancedStatus,
			BounceClass:     e.BounceClass,
			ResponseText:    e.ResponseText,
		})
		status = e.EventType // events are oldest-first, so the last wins
		if e.Provider != "" {
			provider = e.Provider
		}
		if e.RecipientDomain != "" {
			recipient = e.RecipientDomain
		}
	}
	return publicTraceResponse{
		MessageID:       trace.MessageID,
		FromDomain:      trace.FromDomain,
		RecipientDomain: recipient,
		Provider:        provider,
		Status:          status,
		Label:           label,
		Events:          events,
		CheckedAt:       time.Now().UTC().Format(time.RFC3339),
	}
}
