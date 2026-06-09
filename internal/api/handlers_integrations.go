package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	cpanelpkg "github.com/zezekim/mxsentinel/internal/cpanel"
	chstore "github.com/zezekim/mxsentinel/internal/store/clickhouse"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
	whmpkg "github.com/zezekim/mxsentinel/internal/whmcs"
)

// ── response shapes ───────────────────────────────────────────────────────────

type cpanelServerJSON struct {
	ID           string  `json:"id"`
	TenantID     string  `json:"tenant_id"`
	Label        string  `json:"label"`
	Hostname     string  `json:"hostname"`
	Port         int     `json:"port"`
	Username     string  `json:"username"`
	APIToken     string  `json:"api_token"` // always "***" in responses
	VerifySSL    bool    `json:"verify_ssl"`
	SyncInterval int     `json:"sync_interval"`
	LastSyncedAt *string `json:"last_synced_at"`
	SyncStatus   string  `json:"sync_status"`
	SyncError    string  `json:"sync_error,omitempty"`
	AccountCount int     `json:"account_count"`
	CreatedAt    string  `json:"created_at"`
}

type cpanelAccountJSON struct {
	ID            string             `json:"id"`
	ServerID      string             `json:"server_id"`
	Username      string             `json:"username"`
	PrimaryDomain string             `json:"primary_domain"`
	OwnerEmail    string             `json:"owner_email"`
	Plan          string             `json:"plan"`
	Suspended     bool               `json:"suspended"`
	DiskUsedMB    int                `json:"disk_used_mb"`
	SyncedAt      string             `json:"synced_at"`
	Domains       []cpanelDomainJSON `json:"domains,omitempty"`
	Metrics       *cpanelMetricsJSON `json:"metrics,omitempty"`
}

type cpanelDomainJSON struct {
	Domain     string `json:"domain"`
	DomainType string `json:"domain_type"`
}

type cpanelMetricsJSON struct {
	Delivered uint64 `json:"delivered"`
	Deferred  uint64 `json:"deferred"`
	Bounced   uint64 `json:"bounced"`
	Rejected  uint64 `json:"rejected"`
	Total     uint64 `json:"total"`
}

type whmcsConnectionJSON struct {
	ID               string   `json:"id"`
	TenantID         string   `json:"tenant_id"`
	Label            string   `json:"label"`
	APIURL           string   `json:"api_url"`
	APIIdentifier    string   `json:"api_identifier"`
	PushFrequency    string   `json:"push_frequency"`
	PushMetricFields []string `json:"push_metric_fields"`
	Enabled          bool     `json:"enabled"`
	LastPushedAt     *string  `json:"last_pushed_at"`
	CreatedAt        string   `json:"created_at"`
}

type whmcsPushLogJSON struct {
	ID             string `json:"id"`
	PushedAt       string `json:"pushed_at"`
	AccountsPushed int    `json:"accounts_pushed"`
	Status         string `json:"status"`
	ErrorDetail    string `json:"error_detail,omitempty"`
	PeriodStart    string `json:"period_start"`
	PeriodEnd      string `json:"period_end"`
}

// ── helpers ───────────────────────────────────────────────────────────────────

func formatCpanelServer(srv pgstore.CpanelServer) cpanelServerJSON {
	j := cpanelServerJSON{
		ID:           srv.ID,
		TenantID:     srv.TenantID,
		Label:        srv.Label,
		Hostname:     srv.Hostname,
		Port:         srv.Port,
		Username:     srv.Username,
		APIToken:     "***",
		VerifySSL:    srv.VerifySSL,
		SyncInterval: srv.SyncInterval,
		SyncStatus:   srv.SyncStatus,
		SyncError:    srv.SyncError,
		AccountCount: srv.AccountCount,
		CreatedAt:    srv.CreatedAt.UTC().Format(time.RFC3339),
	}
	if srv.LastSyncedAt != nil {
		s := srv.LastSyncedAt.UTC().Format(time.RFC3339)
		j.LastSyncedAt = &s
	}
	return j
}

func formatCpanelAccount(a pgstore.CpanelAccount) cpanelAccountJSON {
	return cpanelAccountJSON{
		ID:            a.ID,
		ServerID:      a.ServerID,
		Username:      a.Username,
		PrimaryDomain: a.PrimaryDomain,
		OwnerEmail:    a.OwnerEmail,
		Plan:          a.Plan,
		Suspended:     a.Suspended,
		DiskUsedMB:    a.DiskUsedMB,
		SyncedAt:      a.SyncedAt.UTC().Format(time.RFC3339),
	}
}

func formatWhmcsConnection(c pgstore.WhmcsConnection) whmcsConnectionJSON {
	j := whmcsConnectionJSON{
		ID:               c.ID,
		TenantID:         c.TenantID,
		Label:            c.Label,
		APIURL:           c.APIURL,
		APIIdentifier:    c.APIIdentifier,
		PushFrequency:    c.PushFrequency,
		PushMetricFields: c.PushMetricFields,
		Enabled:          c.Enabled,
		CreatedAt:        c.CreatedAt.UTC().Format(time.RFC3339),
	}
	if c.LastPushedAt != nil {
		s := c.LastPushedAt.UTC().Format(time.RFC3339)
		j.LastPushedAt = &s
	}
	return j
}

// encSeal encrypts plaintext using s.enc, falling back to plaintext if enc is nil.
func (s *Server) encSeal(plaintext string) (string, error) {
	if s.enc == nil {
		return plaintext, nil
	}
	return s.enc.Seal(plaintext)
}

// encOpen decrypts ciphertext using s.enc, falling back to returning as-is if enc is nil.
func (s *Server) encOpen(ciphertext string) (string, error) {
	if s.enc == nil {
		return ciphertext, nil
	}
	return s.enc.Open(ciphertext)
}

// ── cPanel Server handlers ────────────────────────────────────────────────────

// GET /v1/integrations/cpanel
func (s *Server) handleListCpanelServers(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	servers, err := s.pg.ListCpanelServers(r.Context(), tenant)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to list cPanel servers")
		return
	}
	out := make([]cpanelServerJSON, 0, len(servers))
	for _, srv := range servers {
		out = append(out, formatCpanelServer(srv))
	}
	writeJSON(w, http.StatusOK, map[string]any{"servers": out})
}

// POST /v1/integrations/cpanel
func (s *Server) handleCreateCpanelServer(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	var req struct {
		Label        string `json:"label"`
		Hostname     string `json:"hostname"`
		Port         int    `json:"port"`
		Username     string `json:"username"`
		APIToken     string `json:"api_token"`
		VerifySSL    bool   `json:"verify_ssl"`
		SyncInterval int    `json:"sync_interval"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "request body must be valid JSON")
		return
	}
	if req.Label == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "label is required")
		return
	}
	if req.Hostname == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "hostname is required")
		return
	}
	if req.Username == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "username is required")
		return
	}
	if req.APIToken == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "api_token is required")
		return
	}
	if req.Port == 0 {
		req.Port = 2087
	}
	if req.SyncInterval == 0 {
		req.SyncInterval = 14400
	}

	// Validate credentials by pinging the WHM API.
	client, err := cpanelpkg.New(cpanelpkg.Config{
		Hostname:  req.Hostname,
		Port:      req.Port,
		Username:  req.Username,
		APIToken:  req.APIToken,
		VerifySSL: req.VerifySSL,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_config", err.Error())
		return
	}
	if err := client.Ping(r.Context()); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "ping_failed", "could not connect to cPanel server: "+err.Error())
		return
	}

	encToken, err := s.encSeal(req.APIToken)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to encrypt credentials")
		return
	}

	id, err := s.pg.CreateCpanelServer(r.Context(), tenant, req.Label, req.Hostname, req.Port,
		req.Username, encToken, req.VerifySSL, req.SyncInterval)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to create cPanel server")
		return
	}

	srv, err := s.pg.GetCpanelServer(r.Context(), tenant, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "server created but could not be retrieved")
		return
	}
	writeJSON(w, http.StatusCreated, formatCpanelServer(srv))
}

// GET /v1/integrations/cpanel/{id}
func (s *Server) handleGetCpanelServer(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	id := r.PathValue("id")
	srv, err := s.pg.GetCpanelServer(r.Context(), tenant, id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "cPanel server not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to get cPanel server")
		return
	}
	writeJSON(w, http.StatusOK, formatCpanelServer(srv))
}

// PATCH /v1/integrations/cpanel/{id}
func (s *Server) handleUpdateCpanelServer(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	id := r.PathValue("id")
	var req struct {
		Label        string `json:"label"`
		VerifySSL    bool   `json:"verify_ssl"`
		SyncInterval int    `json:"sync_interval"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "request body must be valid JSON")
		return
	}
	if req.SyncInterval == 0 {
		req.SyncInterval = 14400
	}
	found, err := s.pg.UpdateCpanelServer(r.Context(), tenant, id, req.Label, req.VerifySSL, req.SyncInterval)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to update cPanel server")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "cPanel server not found")
		return
	}
	srv, _ := s.pg.GetCpanelServer(r.Context(), tenant, id)
	writeJSON(w, http.StatusOK, formatCpanelServer(srv))
}

// DELETE /v1/integrations/cpanel/{id}
func (s *Server) handleDeleteCpanelServer(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	id := r.PathValue("id")
	found, err := s.pg.DeleteCpanelServer(r.Context(), tenant, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to delete cPanel server")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "cPanel server not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /v1/integrations/cpanel/{id}/sync
func (s *Server) handleTriggerCpanelSync(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	id := r.PathValue("id")
	srv, err := s.pg.GetCpanelServer(r.Context(), tenant, id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "cPanel server not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to get cPanel server")
		return
	}

	plainToken, err := s.encOpen(srv.APIToken)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to decrypt credentials")
		return
	}

	client, err := cpanelpkg.New(cpanelpkg.Config{
		Hostname:  srv.Hostname,
		Port:      srv.Port,
		Username:  srv.Username,
		APIToken:  plainToken,
		VerifySSL: srv.VerifySSL,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to create cPanel client")
		return
	}

	syncer := cpanelpkg.NewSyncer(client, s.pg, s.log)
	go syncer.Run(context.Background(), srv.ID)

	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true, "server_id": id})
}

// GET /v1/integrations/cpanel/{id}/accounts
func (s *Server) handleListCpanelAccounts(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	id := r.PathValue("id")
	// Verify tenant owns this server.
	if _, err := s.pg.GetCpanelServer(r.Context(), tenant, id); err != nil {
		writeError(w, http.StatusNotFound, "not_found", "cPanel server not found")
		return
	}
	accts, err := s.pg.ListCpanelAccounts(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to list accounts")
		return
	}
	out := make([]cpanelAccountJSON, 0, len(accts))
	for _, a := range accts {
		j := formatCpanelAccount(a)
		// Apply ?suspended filter
		if sus := r.URL.Query().Get("suspended"); sus != "" {
			wantSus := sus == "true"
			if a.Suspended != wantSus {
				continue
			}
		}
		// Apply ?search filter
		if q := r.URL.Query().Get("search"); q != "" {
			if !containsFold(a.Username, q) && !containsFold(a.PrimaryDomain, q) {
				continue
			}
		}
		out = append(out, j)
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": out})
}

// GET /v1/integrations/cpanel/{id}/accounts/{username}
func (s *Server) handleGetCpanelAccount(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	serverID := r.PathValue("id")
	username := r.PathValue("username")
	if _, err := s.pg.GetCpanelServer(r.Context(), tenant, serverID); err != nil {
		writeError(w, http.StatusNotFound, "not_found", "cPanel server not found")
		return
	}
	acct, err := s.pg.GetCpanelAccountByUsername(r.Context(), serverID, username)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "cPanel account not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to get account")
		return
	}

	j := formatCpanelAccount(acct)

	// Fetch domains.
	domains, _ := s.pg.ListAccountDomains(r.Context(), acct.ID)
	j.Domains = make([]cpanelDomainJSON, 0, len(domains))
	for _, d := range domains {
		j.Domains = append(j.Domains, cpanelDomainJSON{Domain: d.Domain, DomainType: d.DomainType})
	}

	// Fetch 30-day metrics.
	domainNames := make([]string, 0, len(domains))
	for _, d := range domains {
		domainNames = append(domainNames, d.Domain)
	}
	since := time.Now().UTC().AddDate(0, 0, -30)
	if rows, err := s.ch.MetricsByDomain(r.Context(), tenant, domainNames, since, time.Time{}); err == nil {
		var agg chstore.DomainMetrics
		for _, row := range rows {
			agg.Delivered += row.Delivered
			agg.Deferred += row.Deferred
			agg.Bounced += row.Bounced
			agg.Rejected += row.Rejected
			agg.Total += row.Total
		}
		j.Metrics = &cpanelMetricsJSON{
			Delivered: agg.Delivered,
			Deferred:  agg.Deferred,
			Bounced:   agg.Bounced,
			Rejected:  agg.Rejected,
			Total:     agg.Total,
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"account": j})
}

// GET /v1/integrations/cpanel/lookup?domain={domain}
func (s *Server) handleCpanelLookup(w http.ResponseWriter, r *http.Request) {
	domain := r.URL.Query().Get("domain")
	if domain == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "domain query parameter is required")
		return
	}
	acct, err := s.pg.LookupDomainAccount(r.Context(), domain)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "domain not found in any synced cPanel server")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "lookup failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"cpanel_server_id": acct.ServerID,
		"account": map[string]any{
			"username":       acct.Username,
			"primary_domain": acct.PrimaryDomain,
			"owner_email":    acct.OwnerEmail,
			"plan":           acct.Plan,
		},
	})
}

// ── WHMCS handlers ────────────────────────────────────────────────────────────

// GET /v1/integrations/whmcs
func (s *Server) handleListWhmcsConnections(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	conns, err := s.pg.ListWhmcsConnections(r.Context(), tenant)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to list WHMCS connections")
		return
	}
	out := make([]whmcsConnectionJSON, 0, len(conns))
	for _, c := range conns {
		out = append(out, formatWhmcsConnection(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"connections": out})
}

// POST /v1/integrations/whmcs
func (s *Server) handleCreateWhmcsConnection(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	var req struct {
		Label            string   `json:"label"`
		APIURL           string   `json:"api_url"`
		APIIdentifier    string   `json:"api_identifier"`
		APISecret        string   `json:"api_secret"`
		PushFrequency    string   `json:"push_frequency"`
		PushMetricFields []string `json:"push_metric_fields"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "request body must be valid JSON")
		return
	}
	if req.Label == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "label is required")
		return
	}
	if req.APIURL == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "api_url is required")
		return
	}
	if req.APIIdentifier == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "api_identifier is required")
		return
	}
	if req.APISecret == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "api_secret is required")
		return
	}
	if req.PushFrequency == "" {
		req.PushFrequency = "daily"
	}
	if req.PushFrequency != "daily" && req.PushFrequency != "weekly" {
		writeError(w, http.StatusBadRequest, "invalid_field", "push_frequency must be 'daily' or 'weekly'")
		return
	}
	if len(req.PushMetricFields) == 0 {
		req.PushMetricFields = []string{"delivered", "bounced", "rejected", "total"}
	}

	// Ping before saving.
	whmcsClient, err := whmpkg.New(whmpkg.Config{
		APIURL:        req.APIURL,
		APIIdentifier: req.APIIdentifier,
		APISecret:     req.APISecret,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_config", err.Error())
		return
	}
	if err := whmcsClient.Ping(r.Context()); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "ping_failed", "could not connect to WHMCS: "+err.Error())
		return
	}

	encSecret, err := s.encSeal(req.APISecret)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to encrypt credentials")
		return
	}

	id, err := s.pg.CreateWhmcsConnection(r.Context(), tenant, req.Label, req.APIURL,
		req.APIIdentifier, encSecret, req.PushFrequency, req.PushMetricFields)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to create WHMCS connection")
		return
	}
	conn, _ := s.pg.GetWhmcsConnection(r.Context(), tenant, id)
	writeJSON(w, http.StatusCreated, formatWhmcsConnection(conn))
}

// PATCH /v1/integrations/whmcs/{id}
func (s *Server) handleUpdateWhmcsConnection(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	id := r.PathValue("id")
	var req struct {
		Label            string   `json:"label"`
		PushFrequency    string   `json:"push_frequency"`
		PushMetricFields []string `json:"push_metric_fields"`
		Enabled          bool     `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "request body must be valid JSON")
		return
	}
	if req.PushFrequency == "" {
		req.PushFrequency = "daily"
	}
	if len(req.PushMetricFields) == 0 {
		req.PushMetricFields = []string{"delivered", "bounced", "rejected", "total"}
	}
	found, err := s.pg.UpdateWhmcsConnection(r.Context(), tenant, id, req.Label, req.PushFrequency, req.PushMetricFields, req.Enabled)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to update WHMCS connection")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "WHMCS connection not found")
		return
	}
	conn, _ := s.pg.GetWhmcsConnection(r.Context(), tenant, id)
	writeJSON(w, http.StatusOK, formatWhmcsConnection(conn))
}

// DELETE /v1/integrations/whmcs/{id}
func (s *Server) handleDeleteWhmcsConnection(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	id := r.PathValue("id")
	found, err := s.pg.DeleteWhmcsConnection(r.Context(), tenant, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to delete WHMCS connection")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "WHMCS connection not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /v1/integrations/whmcs/{id}/push
func (s *Server) handleTriggerWhmcsPush(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	id := r.PathValue("id")
	conn, err := s.pg.GetWhmcsConnection(r.Context(), tenant, id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "WHMCS connection not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to get WHMCS connection")
		return
	}

	plainSecret, err := s.encOpen(conn.APISecret)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to decrypt credentials")
		return
	}

	whmcsClient, err := whmpkg.New(whmpkg.Config{
		APIURL:        conn.APIURL,
		APIIdentifier: conn.APIIdentifier,
		APISecret:     plainSecret,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to create WHMCS client")
		return
	}

	go s.runWhmcsPush(conn.ID, conn.TenantID, whmcsClient)

	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true, "connection_id": id})
}

// runWhmcsPush performs a metrics push for a single WHMCS connection in the background.
func (s *Server) runWhmcsPush(connectionID, tenantID string, whmcsClient *whmpkg.Client) {
	ctx := context.Background()
	periodEnd := time.Now().UTC()
	periodStart := periodEnd.Add(-24 * time.Hour)

	servers, err := s.pg.ListCpanelServers(ctx, tenantID)
	if err != nil {
		s.log.Error("whmcs push: list servers", "connection_id", connectionID, "err", err)
		return
	}

	var metrics []whmpkg.AccountMetrics
	for _, srv := range servers {
		dm, err := s.pg.GetDomainMap(ctx, srv.ID)
		if err != nil {
			continue
		}
		domainNames := make([]string, 0, len(dm))
		for d := range dm {
			domainNames = append(domainNames, d)
		}
		rows, err := s.ch.MetricsByDomain(ctx, tenantID, domainNames, periodStart, periodEnd)
		if err != nil {
			continue
		}
		// Build domain→username map for aggregation.
		usernameMap := map[string]string{}
		for domain, acct := range dm {
			usernameMap[domain] = acct.Username
		}
		agg := chstore.AggregateByAccount(rows, usernameMap)
		// Map back to AccountMetrics with email info.
		for username, m := range agg {
			// Find account info for this username.
			var ownerEmail, primaryDomain string
			for _, acct := range dm {
				if acct.Username == username {
					ownerEmail = acct.OwnerEmail
					primaryDomain = acct.PrimaryDomain
					break
				}
			}
			metrics = append(metrics, whmpkg.AccountMetrics{
				CpanelUsername: username,
				PrimaryDomain:  primaryDomain,
				OwnerEmail:     ownerEmail,
				Delivered:      int64(m.Delivered),
				Bounced:        int64(m.Bounced),
				Rejected:       int64(m.Rejected),
				Deferred:       int64(m.Deferred),
				Total:          int64(m.Total),
				PeriodStart:    periodStart,
				PeriodEnd:      periodEnd,
			})
		}
	}

	pushed, pushErr := whmcsClient.PushMetrics(ctx, metrics)
	status := "ok"
	errDetail := ""
	if pushErr != nil {
		status = "partial"
		errDetail = pushErr.Error()
	}
	if err := s.pg.RecordWhmcsPush(ctx, connectionID, pushed, status, errDetail, periodStart, periodEnd); err != nil {
		s.log.Error("whmcs push: record push", "connection_id", connectionID, "err", err)
	}
	if err := s.pg.MarkWhmcsPushed(ctx, connectionID); err != nil {
		s.log.Error("whmcs push: mark pushed", "connection_id", connectionID, "err", err)
	}
	s.log.Info("whmcs push complete", "connection_id", connectionID, "accounts_pushed", pushed)
}

// GET /v1/integrations/whmcs/{id}/log
func (s *Server) handleWhmcsPushLog(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	id := r.PathValue("id")
	if _, err := s.pg.GetWhmcsConnection(r.Context(), tenant, id); err != nil {
		writeError(w, http.StatusNotFound, "not_found", "WHMCS connection not found")
		return
	}
	limit := parseIntParam(r, "limit", 20, 100)
	logs, err := s.pg.ListWhmcsPushLog(r.Context(), id, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to get push log")
		return
	}
	out := make([]whmcsPushLogJSON, 0, len(logs))
	for _, l := range logs {
		out = append(out, whmcsPushLogJSON{
			ID:             l.ID,
			PushedAt:       l.PushedAt.UTC().Format(time.RFC3339),
			AccountsPushed: l.AccountsPushed,
			Status:         l.Status,
			ErrorDetail:    l.ErrorDetail,
			PeriodStart:    l.PeriodStart.UTC().Format(time.RFC3339),
			PeriodEnd:      l.PeriodEnd.UTC().Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"log": out})
}

// ── Metrics export ────────────────────────────────────────────────────────────

// GET /v1/integrations/metrics?server_id={id}&from={RFC3339}&to={RFC3339}
func (s *Server) handleIntegrationMetrics(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenant(r)
	serverID := r.URL.Query().Get("server_id")
	if serverID == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "server_id is required")
		return
	}
	if _, err := s.pg.GetCpanelServer(r.Context(), tenant, serverID); errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "cPanel server not found")
		return
	}

	var since, until time.Time
	if f := r.URL.Query().Get("from"); f != "" {
		t, err := time.Parse(time.RFC3339, f)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_field", "from must be RFC3339")
			return
		}
		since = t
	}
	if t := r.URL.Query().Get("to"); t != "" {
		u, err := time.Parse(time.RFC3339, t)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_field", "to must be RFC3339")
			return
		}
		until = u
	}
	if since.IsZero() {
		since = time.Now().UTC().AddDate(0, 0, -30)
	}
	if until.IsZero() {
		until = time.Now().UTC()
	}

	dm, err := s.pg.GetDomainMap(r.Context(), serverID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to get domain map")
		return
	}
	domainNames := make([]string, 0, len(dm))
	for d := range dm {
		domainNames = append(domainNames, d)
	}

	rows, err := s.ch.MetricsByDomain(r.Context(), tenant, domainNames, since, until)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to query metrics")
		return
	}

	usernameMap := map[string]string{}
	for domain, acct := range dm {
		usernameMap[domain] = acct.Username
	}
	agg := chstore.AggregateByAccount(rows, usernameMap)

	type accountEntry struct {
		Username      string            `json:"username"`
		PrimaryDomain string            `json:"primary_domain"`
		OwnerEmail    string            `json:"owner_email"`
		Metrics       cpanelMetricsJSON `json:"metrics"`
	}
	accounts := make([]accountEntry, 0, len(agg))
	for username, m := range agg {
		var ownerEmail, primaryDomain string
		for _, acct := range dm {
			if acct.Username == username {
				ownerEmail = acct.OwnerEmail
				primaryDomain = acct.PrimaryDomain
				break
			}
		}
		accounts = append(accounts, accountEntry{
			Username:      username,
			PrimaryDomain: primaryDomain,
			OwnerEmail:    ownerEmail,
			Metrics: cpanelMetricsJSON{
				Delivered: m.Delivered,
				Deferred:  m.Deferred,
				Bounced:   m.Bounced,
				Rejected:  m.Rejected,
				Total:     m.Total,
			},
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"server_id": serverID,
		"period":    map[string]any{"from": since.Format(time.RFC3339), "to": until.Format(time.RFC3339)},
		"accounts":  accounts,
	})
}

// containsFold is a case-insensitive substring check.
func containsFold(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	sl := len(s)
	subl := len(sub)
	if subl > sl {
		return false
	}
	for i := 0; i <= sl-subl; i++ {
		if equalFold(s[i:i+subl], sub) {
			return true
		}
	}
	return false
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 32
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 32
		}
		if ca != cb {
			return false
		}
	}
	return true
}
