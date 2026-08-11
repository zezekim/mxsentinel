package cpanelplugin

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// upstream is a minimal MX Sentinel apid client. It is constructed once in the broker
// and is the *only* holder of the API token in this process.
type upstream struct {
	base string
	http *http.Client

	// mu guards token: the renewal loop rotates it from a background goroutine while
	// request handlers are reading it to sign calls.
	mu    sync.RWMutex
	token string
}

func newUpstream(cfg Config) *upstream {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: !cfg.VerifySSL},
	}
	return &upstream{
		base:  strings.TrimRight(cfg.APIBase, "/"),
		token: cfg.Token,
		http:  &http.Client{Timeout: 20 * time.Second, Transport: tr},
	}
}

// Token returns the bearer token currently in force.
func (u *upstream) Token() string {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.token
}

// SetToken installs a rotated token. Requests already in flight keep signing with the
// old one, which is harmless: the server honours it for its grace window.
func (u *upstream) SetToken(token string) {
	u.mu.Lock()
	u.token = token
	u.mu.Unlock()
}

// httpError carries the upstream status next to the message so callers can tell a dead
// token (401) — which no amount of retrying will fix — from a transient failure. The
// message text is what operators already see, so it is kept verbatim.
type httpError struct {
	status int
	msg    string
}

func (e *httpError) Error() string { return e.msg }

// statusOf reports the upstream HTTP status behind err, or 0 when err did not come from
// a response (dial failure, timeout, decode error).
func statusOf(err error) int {
	var he *httpError
	if errors.As(err, &he) {
		return he.status
	}
	return 0
}

// DomainItem mirrors apid's GET /v1/domains list item (internal/api/handlers_domains.go).
type DomainItem struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Status        string            `json:"status"`
	Categories    map[string]string `json:"categories"`
	Overall       string            `json:"overall"`
	LastCheckedAt *string           `json:"last_checked_at"`
	FindingCount  int               `json:"finding_count"`
}

// Incident mirrors the subset of apid's incidentJSON the plugin renders.
type Incident struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"`
	Severity   string   `json:"severity"`
	Domain     string   `json:"domain"`
	Subject    string   `json:"subject"`
	Title      string   `json:"title"`
	Status     string   `json:"status"`
	Confidence *float64 `json:"confidence"`
	CreatedAt  string   `json:"created_at"`
	ResolvedAt *string  `json:"resolved_at"`
	AISummary  *string  `json:"ai_summary"`
}

// get performs an authenticated GET and decodes the JSON body into out.
func (u *upstream) get(ctx context.Context, path string, out any) error {
	raw, err := u.getRaw(ctx, path)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

// getRaw performs an authenticated GET and returns the raw body.
func (u *upstream) getRaw(ctx context.Context, path string) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+u.Token())
	req.Header.Set("Accept", "application/json")

	resp, err := u.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, &httpError{resp.StatusCode, fmt.Sprintf("upstream %s: token rejected (status %d)", path, resp.StatusCode)}
	}
	if resp.StatusCode >= 400 {
		return nil, &httpError{resp.StatusCode, fmt.Sprintf("upstream %s: status %d", path, resp.StatusCode)}
	}
	return json.RawMessage(body), nil
}

// ListDomains returns all domains visible to the token's tenant.
func (u *upstream) ListDomains(ctx context.Context) ([]DomainItem, error) {
	var out struct {
		Domains []DomainItem `json:"domains"`
	}
	if err := u.get(ctx, "/v1/domains", &out); err != nil {
		return nil, err
	}
	return out.Domains, nil
}

// DomainHealth returns the raw GET /v1/domains/{id}/health payload (categories,
// snapshot, findings). Passed through verbatim so the frontend renders all findings.
func (u *upstream) DomainHealth(ctx context.Context, id string) (json.RawMessage, error) {
	return u.getRaw(ctx, "/v1/domains/"+url.PathEscape(id)+"/health")
}

// ListIncidents returns up to limit incidents, optionally filtered by status.
// Domain filtering is applied by the broker after fetch (the API filters one domain
// at a time, but a user account can own several).
func (u *upstream) ListIncidents(ctx context.Context, status string, limit int) ([]Incident, error) {
	q := url.Values{}
	if status != "" {
		q.Set("status", status)
	}
	if limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", limit))
	}
	var out struct {
		Incidents []Incident `json:"incidents"`
	}
	if err := u.get(ctx, "/v1/incidents?"+q.Encode(), &out); err != nil {
		return nil, err
	}
	return out.Incidents, nil
}

// RBLStatus returns the raw egress-IP health payload (admin view only).
func (u *upstream) RBLStatus(ctx context.Context) (json.RawMessage, error) {
	return u.getRaw(ctx, "/v1/rbl/status")
}

// Deliverability returns the raw tenant-wide provider deliverability (admin only —
// it aggregates across all accounts, so it must never be exposed in a user scope).
func (u *upstream) Deliverability(ctx context.Context) (json.RawMessage, error) {
	return u.getRaw(ctx, "/v1/analytics/deliverability")
}

// postJSON performs an authenticated POST with a JSON body and decodes the response.
func (u *upstream) postJSON(ctx context.Context, path string, body, out any) error {
	return u.sendJSON(ctx, http.MethodPost, path, body, out)
}

// sendJSON performs an authenticated request with a JSON body and decodes the response.
func (u *upstream) sendJSON(ctx context.Context, method, path string, body, out any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, u.base+path, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+u.Token())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := u.http.Do(req)
	if err != nil {
		return fmt.Errorf("call %s: %w", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		// 401 = the token is not recognised at all; 403 = it is valid but under-scoped.
		hint := "token not recognised — check it was copied whole and has not expired or been revoked"
		if resp.StatusCode == http.StatusForbidden {
			hint = `token is missing a scope — this plugin needs "read,relay" (an "admin" token also satisfies both); mint one with: mxctl apikey create --tenant <slug> --scopes read,relay --name cpanel-plugin`
		}
		return &httpError{resp.StatusCode, fmt.Sprintf("upstream %s: token rejected (status %d): %s", path, resp.StatusCode, hint)}
	}
	if resp.StatusCode >= 400 {
		// Surface the API's error message when present.
		var e struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &e)
		detail := strings.TrimSpace(e.Message + " " + e.Error)
		if detail == "" {
			detail = fmt.Sprintf("status %d", resp.StatusCode)
		}
		return &httpError{resp.StatusCode, fmt.Sprintf("upstream %s: %s", path, detail)}
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("decode %s: %w", path, err)
		}
	}
	return nil
}

// Settings mirrors the subset of apid's GET /v1/settings the relay installer needs
// (internal/api/handlers_settings.go).
type Settings struct {
	SPFInclude   string `json:"spf_include"`
	DKIMSelector string `json:"dkim_selector"`
	DMARCPolicy  string `json:"dmarc_policy"`
	DMARCRua     string `json:"dmarc_rua"`
	RelayHost    string `json:"relay_host"`
	RelayPort    int    `json:"relay_port"`
}

// GetSettings returns the tenant's relay + DNS defaults. apid wraps the payload as
// {"settings": {...}}, so decode the wrapper.
func (u *upstream) GetSettings(ctx context.Context) (Settings, error) {
	var out struct {
		Settings Settings `json:"settings"`
	}
	err := u.get(ctx, "/v1/settings", &out)
	return out.Settings, err
}

// SMTPUser mirrors apid's smtpUserJSON (internal/api/handlers_smtp_users.go).
type SMTPUser struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Domain   string `json:"domain"`
	Enabled  bool   `json:"enabled"`
}

// ListSMTPUsers returns the tenant's SMTP submission users (for reuse/idempotency).
func (u *upstream) ListSMTPUsers(ctx context.Context) ([]SMTPUser, error) {
	var out struct {
		Users []SMTPUser `json:"users"`
	}
	if err := u.get(ctx, "/v1/smtp-users", &out); err != nil {
		return nil, err
	}
	return out.Users, nil
}

// CreateSMTPUser provisions a SASL submission credential on the relay (relay scope).
func (u *upstream) CreateSMTPUser(ctx context.Context, username, password, domain string) (SMTPUser, error) {
	var out SMTPUser
	err := u.postJSON(ctx, "/v1/smtp-users", map[string]string{
		"username": username,
		"password": password,
		"domain":   domain,
	}, &out)
	return out, err
}

// ResetSMTPUserPassword sets a new password on an existing SMTP user (relay scope).
func (u *upstream) ResetSMTPUserPassword(ctx context.Context, id, password string) error {
	return u.sendJSON(ctx, http.MethodPatch, "/v1/smtp-users/"+url.PathEscape(id),
		map[string]string{"password": password}, nil)
}

// MeResponse mirrors apid's GET /v1/me. ExpiresAt is a pointer because the field is
// ABSENT for a credential that never expires — "no expiry" and "empty string" are
// different answers, and only the first one means "there is nothing to renew".
type MeResponse struct {
	TenantID       string   `json:"tenant_id"`
	Scopes         []string `json:"scopes"`
	UserID         string   `json:"user_id"`
	Role           string   `json:"role"`
	CredentialName string   `json:"credential_name"`
	ExpiresAt      *string  `json:"expires_at"`
}

// Expiry parses ExpiresAt. ok is false when the credential never expires.
func (m MeResponse) Expiry() (t time.Time, ok bool, err error) {
	if m.ExpiresAt == nil || strings.TrimSpace(*m.ExpiresAt) == "" {
		return time.Time{}, false, nil
	}
	raw := strings.TrimSpace(*m.ExpiresAt)
	t, err = time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("parse expires_at %q: %w", raw, err)
	}
	return t, true, nil
}

// Me returns who the current token is, including when it expires.
func (u *upstream) Me(ctx context.Context) (MeResponse, error) {
	var out MeResponse
	err := u.get(ctx, "/v1/me", &out)
	return out, err
}

// RenewedKey mirrors apid's POST /v1/apikeys/renew response. Token is the only copy of
// the new secret that will ever exist outside this process — the server keeps a hash.
type RenewedKey struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Token     string   `json:"token"`
	Prefix    string   `json:"prefix"`
	Scopes    []string `json:"scopes"`
	ExpiresAt string   `json:"expires_at"`
}

// RenewToken rotates this credential in place: same name, same scopes, new secret. No
// scope is required because renewing yourself grants nothing new. The old secret keeps
// working for a short grace window, which is what makes a crash between this call and
// persisting the result recoverable instead of fatal.
func (u *upstream) RenewToken(ctx context.Context) (RenewedKey, error) {
	var out RenewedKey
	// The endpoint takes no request body — identity comes from the bearer token.
	err := u.postJSON(ctx, "/v1/apikeys/renew", struct{}{}, &out)
	return out, err
}
