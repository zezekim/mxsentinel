// Package api implements the MX Sentinel REST API (v1): domain health, DNS drift, the
// message explorer, and DMARC reports. It uses the standard library net/http router
// (Go 1.22 method+path patterns) — no third-party router. See docs/api-v1.md and
// docs/phase-1-plan.md WS6.
package api

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/zezekim/mxsentinel/internal/auth"
	"github.com/zezekim/mxsentinel/internal/crypto"
	dnsx "github.com/zezekim/mxsentinel/internal/dns"
	chstore "github.com/zezekim/mxsentinel/internal/store/clickhouse"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
)

// Server holds the API dependencies.
type Server struct {
	pg            *pgstore.Store
	ch            *chstore.Store
	resolver      dnsx.Resolver
	log           *slog.Logger
	corsOrigin    string
	limiter       Limiter           // nil disables rate limiting
	sessions      auth.SessionStore // nil disables user login
	enc           *crypto.Encryptor // nil = passthrough (plaintext credentials)
	publicBaseURL string            // e.g. https://sentinel.squidix.net; "" → return relative /trace paths
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
	mux.HandleFunc("POST /v1/me/password", s.handleChangePassword)
	mux.HandleFunc("GET /v1/domains", s.requireScope(ScopeRead, s.handleListDomains))
	mux.HandleFunc("POST /v1/domains", s.requireScope(ScopeWrite, s.handleCreateDomain))
	mux.HandleFunc("DELETE /v1/domains/{id}", s.requireScope(ScopeAdmin, s.handleDeleteDomain))
	mux.HandleFunc("PATCH /v1/domains/{id}", s.requireScope(ScopeWrite, s.handleUpdateDomain))
	mux.HandleFunc("GET /v1/domains/{id}/health", s.requireScope(ScopeRead, s.handleDomainHealth))
	mux.HandleFunc("GET /v1/domains/{id}/dns/snapshots", s.requireScope(ScopeRead, s.handleSnapshots))
	mux.HandleFunc("POST /v1/domains/{id}/dns/recheck", s.requireScope(ScopeWrite, s.handleRecheck))
	mux.HandleFunc("GET /v1/messages", s.requireScope(ScopeRead, s.handleMessages))
	// Per-message shareable trace links (the message is keyed by relay queue id).
	mux.HandleFunc("POST /v1/messages/{queueID}/share", s.requireScope(ScopeWrite, s.handleCreateShareLink))
	mux.HandleFunc("GET /v1/messages/{queueID}/shares", s.requireScope(ScopeRead, s.handleListShareLinks))
	mux.HandleFunc("DELETE /v1/messages/shares/{id}", s.requireScope(ScopeWrite, s.handleRevokeShareLink))
	mux.HandleFunc("GET /v1/dmarc/reports", s.requireScope(ScopeRead, s.handleDMARCReports))
	mux.HandleFunc("GET /v1/dmarc/reports.csv", s.requireScope(ScopeRead, s.handleDMARCReportsCSV))
	mux.HandleFunc("GET /v1/dmarc/reports/{id}/records", s.requireScope(ScopeRead, s.handleDMARCReportRecords))
	mux.HandleFunc("GET /v1/dmarc/reports/{id}/records.csv", s.requireScope(ScopeRead, s.handleDMARCRecordsCSV))
	mux.HandleFunc("GET /v1/dmarc/analytics", s.requireScope(ScopeRead, s.handleDMARCAnalytics))
	mux.HandleFunc("GET /v1/dmarc/trend", s.requireScope(ScopeRead, s.handleDMARCTrend))
	mux.HandleFunc("GET /v1/dmarc/sources", s.requireScope(ScopeRead, s.handleDMARCSources))
	mux.HandleFunc("GET /v1/dmarc/threats", s.requireScope(ScopeRead, s.handleDMARCThreats))
	mux.HandleFunc("GET /v1/dmarc/policy-advice", s.requireScope(ScopeRead, s.handleDMARCPolicyAdvice))
	mux.HandleFunc("GET /v1/analytics/deliverability", s.requireScope(ScopeRead, s.handleDeliverability))
	mux.HandleFunc("GET /v1/analytics/rejections", s.requireScope(ScopeRead, s.handleRejections))
	mux.HandleFunc("GET /v1/analytics/top-senders", s.requireScope(ScopeRead, s.handleTopSenders))
	mux.HandleFunc("GET /v1/rbl/status", s.requireScope(ScopeRead, s.handleRBLStatus))
	mux.HandleFunc("GET /v1/anomaly/recent", s.requireScope(ScopeRead, s.handleAnomalyRecent))
	mux.HandleFunc("GET /v1/reputation", s.requireScope(ScopeRead, s.handleReputation))
	mux.HandleFunc("GET /v1/auth-security", s.requireScope(ScopeRead, s.handleAuthSecurity))
	mux.HandleFunc("POST /v1/auth-security/{user}/lock", s.requireScope(ScopeAdmin, s.handleLockCredential))
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
	// Alert rules & notification channels
	mux.HandleFunc("GET /v1/alert-rules", s.requireScope(ScopeRead, s.handleListAlertRules))
	mux.HandleFunc("POST /v1/alert-rules", s.requireScope(ScopeWrite, s.handleCreateAlertRule))
	mux.HandleFunc("PATCH /v1/alert-rules/{id}", s.requireScope(ScopeWrite, s.handleUpdateAlertRule))
	mux.HandleFunc("DELETE /v1/alert-rules/{id}", s.requireScope(ScopeAdmin, s.handleDeleteAlertRule))
	mux.HandleFunc("GET /v1/notification-channels", s.requireScope(ScopeRead, s.handleListChannels))
	mux.HandleFunc("POST /v1/notification-channels", s.requireScope(ScopeWrite, s.handleCreateChannel))
	mux.HandleFunc("DELETE /v1/notification-channels/{id}", s.requireScope(ScopeAdmin, s.handleDeleteChannel))
	mux.HandleFunc("GET /v1/alert-events", s.requireScope(ScopeRead, s.handleListAlertEvents))
	// Delivery heatmap
	mux.HandleFunc("GET /v1/heatmap", s.requireScope(ScopeRead, s.handleHeatmap))
	mux.HandleFunc("GET /v1/analytics/per-user", s.requireScope(ScopeRead, s.handlePerUserStats))
	// DKIM key rotation
	mux.HandleFunc("GET /v1/domains/{id}/dkim/plans", s.requireScope(ScopeRead, s.handleListDKIMPlans))
	mux.HandleFunc("POST /v1/domains/{id}/dkim/plans", s.requireScope(ScopeWrite, s.handleCreateDKIMPlan))
	mux.HandleFunc("PATCH /v1/domains/{id}/dkim/plans/{planID}", s.requireScope(ScopeWrite, s.handleUpdateDKIMPlan))
	mux.HandleFunc("DELETE /v1/domains/{id}/dkim/plans/{planID}", s.requireScope(ScopeAdmin, s.handleDeleteDKIMPlan))
	// Warm-up advisor
	mux.HandleFunc("GET /v1/warmup", s.requireScope(ScopeRead, s.handleListWarmupPlans))
	mux.HandleFunc("POST /v1/warmup", s.requireScope(ScopeWrite, s.handleCreateWarmupPlan))
	mux.HandleFunc("GET /v1/warmup/{id}", s.requireScope(ScopeRead, s.handleGetWarmupPlan))
	mux.HandleFunc("PATCH /v1/warmup/{id}", s.requireScope(ScopeWrite, s.handleUpdateWarmupPlan))
	mux.HandleFunc("DELETE /v1/warmup/{id}", s.requireScope(ScopeAdmin, s.handleDeleteWarmupPlan))
	// Scheduled reports
	mux.HandleFunc("GET /v1/reports", s.requireScope(ScopeRead, s.handleListReports))
	mux.HandleFunc("POST /v1/reports", s.requireScope(ScopeWrite, s.handleCreateReport))
	mux.HandleFunc("PUT /v1/reports/{id}", s.requireScope(ScopeWrite, s.handleUpdateReport))
	mux.HandleFunc("DELETE /v1/reports/{id}", s.requireScope(ScopeAdmin, s.handleDeleteReport))
	mux.HandleFunc("POST /v1/reports/{id}/send-now", s.requireScope(ScopeWrite, s.handleSendReportNow))
	// Auth security detail
	mux.HandleFunc("GET /v1/auth-security/{user}/stats", s.requireScope(ScopeRead, s.handleAuthUserStats))
	// cPanel / WHMCS integrations
	mux.HandleFunc("GET /v1/integrations/cpanel", s.requireScope(ScopeRead, s.handleListCpanelServers))
	mux.HandleFunc("POST /v1/integrations/cpanel", s.requireScope(ScopeAdmin, s.handleCreateCpanelServer))
	mux.HandleFunc("GET /v1/integrations/cpanel/lookup", s.requireScope(ScopeRead, s.handleCpanelLookup))
	mux.HandleFunc("GET /v1/integrations/cpanel/{id}", s.requireScope(ScopeRead, s.handleGetCpanelServer))
	mux.HandleFunc("PATCH /v1/integrations/cpanel/{id}", s.requireScope(ScopeAdmin, s.handleUpdateCpanelServer))
	mux.HandleFunc("DELETE /v1/integrations/cpanel/{id}", s.requireScope(ScopeAdmin, s.handleDeleteCpanelServer))
	mux.HandleFunc("POST /v1/integrations/cpanel/{id}/sync", s.requireScope(ScopeAdmin, s.handleTriggerCpanelSync))
	mux.HandleFunc("GET /v1/integrations/cpanel/{id}/accounts", s.requireScope(ScopeRead, s.handleListCpanelAccounts))
	mux.HandleFunc("GET /v1/integrations/cpanel/{id}/accounts/{username}", s.requireScope(ScopeRead, s.handleGetCpanelAccount))
	mux.HandleFunc("GET /v1/integrations/whmcs", s.requireScope(ScopeRead, s.handleListWhmcsConnections))
	mux.HandleFunc("POST /v1/integrations/whmcs", s.requireScope(ScopeAdmin, s.handleCreateWhmcsConnection))
	mux.HandleFunc("PATCH /v1/integrations/whmcs/{id}", s.requireScope(ScopeAdmin, s.handleUpdateWhmcsConnection))
	mux.HandleFunc("DELETE /v1/integrations/whmcs/{id}", s.requireScope(ScopeAdmin, s.handleDeleteWhmcsConnection))
	mux.HandleFunc("POST /v1/integrations/whmcs/{id}/push", s.requireScope(ScopeAdmin, s.handleTriggerWhmcsPush))
	mux.HandleFunc("GET /v1/integrations/whmcs/{id}/log", s.requireScope(ScopeRead, s.handleWhmcsPushLog))
	mux.HandleFunc("GET /v1/integrations/metrics", s.requireScope(ScopeRead, s.handleIntegrationMetrics))

	// Feature routes (each registrar lives in its own handlers_*.go file).
	s.registerHealthScoreRoutes(mux)  // deliverability health score
	s.registerTLSReportingRoutes(mux) // TLS-RPT + MTA-STS
	s.registerBounceRoutes(mux)       // bounce classification + suppression
	s.registerMicrosoftRoutes(mux)    // Microsoft SNDS + JMRP
	s.registerSeedTestRoutes(mux)     // inbox-placement seed testing
	s.registerSMTPProbeRoutes(mux)    // synthetic SMTP probing
	s.registerNLQueryRoutes(mux)      // natural-language analytics
	s.registerBIMIRoutes(mux)         // BIMI / VMC validation
	s.registerAlertChannelRoutes(mux) // alert delivery channels

	// The authed pipeline: auth → rate limit (per tenant) → audit (records mutations).
	authed := chain(mux, s.requireAuth, s.rateLimit, s.auditWrites)

	// Login is public (it issues tokens), so it sits outside requireAuth. The more
	// specific pattern wins, so everything else falls through to the authed pipeline.
	root := http.NewServeMux()
	root.HandleFunc("POST /v1/auth/login", s.handleLogin)
	root.HandleFunc("GET /v1/status/{slug}", s.handlePublicStatus)
	root.HandleFunc("GET /v1/trace/{token}", s.handlePublicTrace)
	root.Handle("/", authed)

	// recoverer → logger → cors (handles preflight) → (login | authed routes)
	return chain(root, s.recoverer, s.requestLogger, s.cors)
}

// WithEncryptor wires in the credential encryptor. If not called, credentials are stored
// as plaintext (passthrough mode — acceptable for dev; production requires MXS_ENCRYPTION_KEY).
func (s *Server) WithEncryptor(enc *crypto.Encryptor) *Server {
	s.enc = enc
	return s
}

// WithPublicBaseURL sets the public origin used to build shareable message-trace URLs
// (e.g. https://sentinel.squidix.net). When empty, share responses return only the
// relative "/trace/<token>" path and the caller composes its own origin.
func (s *Server) WithPublicBaseURL(base string) *Server {
	s.publicBaseURL = strings.TrimRight(base, "/")
	return s
}

// tenant returns the authenticated tenant id; handlers call this after requireAuth.
func (s *Server) tenant(r *http.Request) string {
	a, _ := authFromContext(r.Context())
	return a.TenantID
}
