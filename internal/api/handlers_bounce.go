package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/zezekim/mxsentinel/internal/bounce"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
)

// registerBounceRoutes wires the bounce-classification + suppression-list endpoints. The
// orchestrator calls this from server.go's Handler().
//
//	GET    /v1/bounces              (read)  classified bounce feed + per-domain rates + category totals
//	GET    /v1/suppression          (read)  list a tenant's suppression entries
//	POST   /v1/suppression          (write) add/refresh a suppression entry
//	DELETE /v1/suppression/{hash}   (write) remove a suppression entry by recipient hash
//	GET    /v1/suppression/export   (read)  relay-syncable export (plain hash list | postfix access map)
func (s *Server) registerBounceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/bounces", s.requireScope(ScopeRead, s.handleBounces))
	mux.HandleFunc("GET /v1/suppression", s.requireScope(ScopeRead, s.handleListSuppression))
	mux.HandleFunc("POST /v1/suppression", s.requireScope(ScopeWrite, s.handleAddSuppression))
	mux.HandleFunc("DELETE /v1/suppression/{hash}", s.requireScope(ScopeWrite, s.handleDeleteSuppression))
	mux.HandleFunc("GET /v1/suppression/export", s.requireScope(ScopeRead, s.handleExportSuppression))
}

// ---- response shapes -------------------------------------------------------

type classifiedBounceJSON struct {
	EventTime       string `json:"event_time"`
	FromDomain      string `json:"from_domain"`
	RecipientDomain string `json:"recipient_domain"`
	RecipientHash   string `json:"recipient_hash"`
	Provider        string `json:"provider"`
	SMTPCode        uint16 `json:"smtp_code"`
	EnhancedStatus  string `json:"enhanced_status"`
	ResponseText    string `json:"response_text"`
	Category        string `json:"category"`
	Suppressed      bool   `json:"suppressed"` // whether this category auto-suppresses the recipient
}

type domainRateJSON struct {
	Domain  string  `json:"domain"`
	Total   uint64  `json:"total"`
	Bounced uint64  `json:"bounced"`
	Rate    float64 `json:"rate"`
}

type categoryCountJSON struct {
	Category string `json:"category"`
	Count    int    `json:"count"`
}

type bouncesResponse struct {
	Window      string                 `json:"window"`
	Categories  []categoryCountJSON    `json:"categories"`
	DomainRates []domainRateJSON       `json:"domain_rates"`
	Recent      []classifiedBounceJSON `json:"recent"`
}

type suppressionEntryJSON struct {
	RecipientHash string  `json:"recipient_hash"`
	Reason        string  `json:"reason"`
	Category      string  `json:"category"`
	Source        string  `json:"source"`
	CreatedAt     string  `json:"created_at"`
	ExpiresAt     *string `json:"expires_at"`
}

type suppressionResponse struct {
	Entries []suppressionEntryJSON `json:"entries"`
	Count   int                    `json:"count"`
}

// ---- handlers --------------------------------------------------------------

// GET /v1/bounces?window=24h — classified bounce feed, per-domain rates, category totals.
func (s *Server) handleBounces(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	window := r.URL.Query().Get("window")
	since := windowSince(window)

	rawRows, err := s.ch.RecentBounces(r.Context(), tenant, since, 500)
	if err != nil {
		s.log.Error("bounces: recent", "tenant_id", tenant, "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to load recent bounces")
		return
	}
	recent := make([]classifiedBounceJSON, 0, len(rawRows))
	for _, b := range rawRows {
		cat := bounce.Classify(int(b.SMTPCode), b.EnhancedStatus, b.ResponseText)
		recent = append(recent, classifiedBounceJSON{
			EventTime:       b.EventTime.UTC().Format(time.RFC3339),
			FromDomain:      b.FromDomain,
			RecipientDomain: b.RecipientDomain,
			RecipientHash:   b.RecipientHash,
			Provider:        b.Provider,
			SMTPCode:        b.SMTPCode,
			EnhancedStatus:  b.EnhancedStatus,
			ResponseText:    b.ResponseText,
			Category:        string(cat),
			Suppressed:      bounce.SuppressionFor(cat).Suppress,
		})
	}

	rates, err := s.ch.DomainBounceRates(r.Context(), tenant, since, 50)
	if err != nil {
		s.log.Error("bounces: domain rates", "tenant_id", tenant, "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to load bounce rates")
		return
	}
	domainRates := make([]domainRateJSON, 0, len(rates))
	for _, d := range rates {
		domainRates = append(domainRates, domainRateJSON{Domain: d.Domain, Total: d.Total, Bounced: d.Bounced, Rate: d.Rate})
	}

	totals, err := s.pg.BounceCategoryTotals(r.Context(), tenant, since)
	if err != nil {
		s.log.Error("bounces: category totals", "tenant_id", tenant, "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to load category totals")
		return
	}
	categories := make([]categoryCountJSON, 0, len(totals))
	for _, c := range totals {
		categories = append(categories, categoryCountJSON{Category: c.Category, Count: c.Count})
	}

	writeJSON(w, http.StatusOK, bouncesResponse{
		Window:      normalizeWindow(window),
		Categories:  categories,
		DomainRates: domainRates,
		Recent:      recent,
	})
}

// GET /v1/suppression?include_expired=false — list a tenant's suppression entries.
func (s *Server) handleListSuppression(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	includeExpired := r.URL.Query().Get("include_expired") == "true"

	entries, err := s.pg.ListSuppression(r.Context(), tenant, includeExpired, 1000)
	if err != nil {
		s.log.Error("list suppression", "tenant_id", tenant, "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to list suppression entries")
		return
	}
	out := make([]suppressionEntryJSON, 0, len(entries))
	for _, e := range entries {
		out = append(out, formatSuppressionEntry(e))
	}
	writeJSON(w, http.StatusOK, suppressionResponse{Entries: out, Count: len(out)})
}

// POST /v1/suppression — add or refresh a suppression entry. Body accepts either a
// recipient_hash directly, or an email that is hashed server-side with the SAME keyed
// HMAC-SHA256 the telemetry parser uses (MXS_RECIPIENT_HASH_KEY), so a manually-suppressed
// address matches what the relay computes.
func (s *Server) handleAddSuppression(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	var req struct {
		RecipientHash string `json:"recipient_hash"`
		Email         string `json:"email"`
		Reason        string `json:"reason"`
		Category      string `json:"category"`
		Source        string `json:"source"`
		TTLHours      int    `json:"ttl_hours"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "request body must be valid JSON")
		return
	}

	hash := strings.TrimSpace(req.RecipientHash)
	if hash == "" && strings.TrimSpace(req.Email) != "" {
		hash = hashRecipient(req.Email)
	}
	if hash == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "recipient_hash or email is required")
		return
	}

	source := req.Source
	if source == "" {
		source = bounce.SourceManual
	}
	reason := req.Reason
	if reason == "" {
		reason = "manual"
	}
	var expiresAt *time.Time
	if req.TTLHours > 0 {
		t := time.Now().Add(time.Duration(req.TTLHours) * time.Hour).UTC()
		expiresAt = &t
	}

	if _, err := s.pg.UpsertSuppression(r.Context(), tenant, hash, reason, req.Category, source, expiresAt); err != nil {
		s.log.Error("add suppression", "tenant_id", tenant, "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to add suppression entry")
		return
	}
	var expStr *string
	if expiresAt != nil {
		e := expiresAt.Format(time.RFC3339)
		expStr = &e
	}
	writeJSON(w, http.StatusCreated, suppressionEntryJSON{
		RecipientHash: hash, Reason: reason, Category: req.Category, Source: source,
		CreatedAt: time.Now().UTC().Format(time.RFC3339), ExpiresAt: expStr,
	})
}

// DELETE /v1/suppression/{hash} — remove a suppression entry by recipient hash.
func (s *Server) handleDeleteSuppression(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	hash := strings.TrimSpace(r.PathValue("hash"))
	if hash == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "recipient hash is required")
		return
	}
	deleted, err := s.pg.DeleteSuppression(r.Context(), tenant, hash)
	if err != nil {
		s.log.Error("delete suppression", "tenant_id", tenant, "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to delete suppression entry")
		return
	}
	if !deleted {
		writeError(w, http.StatusNotFound, "not_found", "suppression entry not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

// GET /v1/suppression/export?format=plain|postfix — relay-syncable export of the active
// suppression list. Returns text/plain (a hash list or a Postfix access map).
func (s *Server) handleExportSuppression(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	format := r.URL.Query().Get("format")
	switch format {
	case bounce.ExportFormatPostfix, bounce.ExportFormatPlain:
	case "":
		format = bounce.ExportFormatPlain
	default:
		writeError(w, http.StatusBadRequest, "invalid_field", "format must be one of: plain, postfix")
		return
	}

	entries, err := s.pg.ActiveSuppressionHashes(r.Context(), tenant)
	if err != nil {
		s.log.Error("export suppression", "tenant_id", tenant, "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to export suppression list")
		return
	}
	records := make([]bounce.SuppressionRecord, 0, len(entries))
	for _, e := range entries {
		records = append(records, bounce.SuppressionRecord{
			RecipientHash: e.RecipientHash, Reason: e.Reason, Category: e.Category,
			Source: e.Source, ExpiresAt: e.ExpiresAt,
		})
	}
	body := bounce.BuildExport(format, records, time.Now())

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\"suppression-"+format+".txt\"")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

// ---- helpers ---------------------------------------------------------------

func formatSuppressionEntry(e pgstore.SuppressionEntry) suppressionEntryJSON {
	var exp *string
	if e.ExpiresAt != nil {
		s := e.ExpiresAt.UTC().Format(time.RFC3339)
		exp = &s
	}
	return suppressionEntryJSON{
		RecipientHash: e.RecipientHash,
		Reason:        e.Reason,
		Category:      e.Category,
		Source:        e.Source,
		CreatedAt:     e.CreatedAt.UTC().Format(time.RFC3339),
		ExpiresAt:     exp,
	}
}

// windowSince converts a window token (1h|24h|7d|30d) to a start time. Defaults to 24h.
func windowSince(window string) time.Time {
	now := time.Now()
	switch window {
	case "1h":
		return now.Add(-1 * time.Hour)
	case "7d":
		return now.Add(-7 * 24 * time.Hour)
	case "30d":
		return now.Add(-30 * 24 * time.Hour)
	default: // 24h
		return now.Add(-24 * time.Hour)
	}
}

func normalizeWindow(window string) string {
	switch window {
	case "1h", "7d", "30d":
		return window
	default:
		return "24h"
	}
}

// hashRecipient mirrors internal/telemetry's hasher so a manually-suppressed email keys the
// same way the relay/telemetry computes it. Key comes from MXS_RECIPIENT_HASH_KEY (raw
// bytes); with no key it falls back to plain SHA-256 (still non-reversible), exactly as the
// parser does.
func hashRecipient(addr string) string {
	addr = strings.ToLower(strings.TrimSpace(addr))
	if addr == "" {
		return ""
	}
	if key := os.Getenv("MXS_RECIPIENT_HASH_KEY"); key != "" {
		m := hmac.New(sha256.New, []byte(key))
		_, _ = m.Write([]byte(addr))
		return hex.EncodeToString(m.Sum(nil))
	}
	sum := sha256.Sum256([]byte(addr))
	return hex.EncodeToString(sum[:])
}
