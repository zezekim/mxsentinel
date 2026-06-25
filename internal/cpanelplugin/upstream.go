package cpanelplugin

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// upstream is a minimal MX Sentinel apid client. It is constructed once in the broker
// and is the *only* holder of the API token in this process.
type upstream struct {
	base  string
	token string
	http  *http.Client
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
	req.Header.Set("Authorization", "Bearer "+u.token)
	req.Header.Set("Accept", "application/json")

	resp, err := u.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("upstream %s: token rejected (status %d)", path, resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("upstream %s: status %d", path, resp.StatusCode)
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
