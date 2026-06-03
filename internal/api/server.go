// Package api implements the MX Sentinel REST API (v1): domain health, DNS drift, the
// message explorer, and DMARC reports. It uses the standard library net/http router
// (Go 1.22 method+path patterns) — no third-party router. See docs/api-v1.md and
// docs/phase-1-plan.md WS6.
package api

import (
	"log/slog"
	"net/http"

	"github.com/zezekim/mxsentinel/internal/auth"
	dnsx "github.com/zezekim/mxsentinel/internal/dns"
	chstore "github.com/zezekim/mxsentinel/internal/store/clickhouse"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
)

// Server holds the API dependencies.
type Server struct {
	pg         *pgstore.Store
	ch         *chstore.Store
	resolver   dnsx.Resolver
	log        *slog.Logger
	corsOrigin string
	limiter    Limiter           // nil disables rate limiting
	sessions   auth.SessionStore // nil disables user login
}

// New constructs the API server. corsOrigin is the Access-Control-Allow-Origin value
// (use "*" for local dev, or the dashboard origin). limiter may be nil to disable
// per-tenant rate limiting; sessions may be nil to disable user login.
func New(pg *pgstore.Store, ch *chstore.Store, resolver dnsx.Resolver, log *slog.Logger, corsOrigin string, limiter Limiter, sessions auth.SessionStore) *Server {
	if corsOrigin == "" {
		corsOrigin = "*"
	}
	return &Server{pg: pg, ch: ch, resolver: resolver, log: log, corsOrigin: corsOrigin, limiter: limiter, sessions: sessions}
}

// Handler returns the fully-wired HTTP handler (routes + middleware).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	// Reads require the "read" scope; mutations require "write" (admin satisfies both).
	mux.HandleFunc("GET /v1/me", s.requireScope(ScopeRead, s.handleMe))
	mux.HandleFunc("GET /v1/domains", s.requireScope(ScopeRead, s.handleListDomains))
	mux.HandleFunc("GET /v1/domains/{id}/health", s.requireScope(ScopeRead, s.handleDomainHealth))
	mux.HandleFunc("GET /v1/domains/{id}/dns/snapshots", s.requireScope(ScopeRead, s.handleSnapshots))
	mux.HandleFunc("POST /v1/domains/{id}/dns/recheck", s.requireScope(ScopeWrite, s.handleRecheck))
	mux.HandleFunc("GET /v1/messages", s.requireScope(ScopeRead, s.handleMessages))
	mux.HandleFunc("GET /v1/dmarc/reports", s.requireScope(ScopeRead, s.handleDMARCReports))
	mux.HandleFunc("GET /v1/analytics/deliverability", s.requireScope(ScopeRead, s.handleDeliverability))
	mux.HandleFunc("GET /v1/analytics/rejections", s.requireScope(ScopeRead, s.handleRejections))
	mux.HandleFunc("GET /v1/incidents", s.requireScope(ScopeRead, s.handleListIncidents))
	mux.HandleFunc("POST /v1/incidents/{id}/resolve", s.requireScope(ScopeWrite, s.handleResolveIncident))
	mux.HandleFunc("GET /v1/audit", s.requireScope(ScopeRead, s.handleAudit))
	mux.HandleFunc("POST /v1/auth/logout", s.handleLogout)
	mux.HandleFunc("GET /v1/users", s.requireScope(ScopeAdmin, s.handleListUsers))
	mux.HandleFunc("POST /v1/users", s.requireScope(ScopeAdmin, s.handleCreateUser))
	mux.HandleFunc("GET /v1/smtp-users", s.requireScope(ScopeAdmin, s.handleListSMTPUsers))
	mux.HandleFunc("POST /v1/smtp-users", s.requireScope(ScopeAdmin, s.handleCreateSMTPUser))
	mux.HandleFunc("PATCH /v1/smtp-users/{id}", s.requireScope(ScopeAdmin, s.handleUpdateSMTPUser))
	mux.HandleFunc("DELETE /v1/smtp-users/{id}", s.requireScope(ScopeAdmin, s.handleDeleteSMTPUser))
	mux.HandleFunc("GET /v1/settings", s.requireScope(ScopeRead, s.handleGetSettings))
	mux.HandleFunc("PUT /v1/settings", s.requireScope(ScopeAdmin, s.handleUpdateSettings))

	// The authed pipeline: auth → rate limit (per tenant) → audit (records mutations).
	authed := chain(mux, s.requireAuth, s.rateLimit, s.auditWrites)

	// Login is public (it issues tokens), so it sits outside requireAuth. The more
	// specific pattern wins, so everything else falls through to the authed pipeline.
	root := http.NewServeMux()
	root.HandleFunc("POST /v1/auth/login", s.handleLogin)
	root.Handle("/", authed)

	// recoverer → logger → cors (handles preflight) → (login | authed routes)
	return chain(root, s.recoverer, s.requestLogger, s.cors)
}

// tenant returns the authenticated tenant id; handlers call this after requireAuth.
func (s *Server) tenant(r *http.Request) string {
	a, _ := authFromContext(r.Context())
	return a.TenantID
}
