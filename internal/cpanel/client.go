package cpanel

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Config holds connection parameters for one WHM server.
type Config struct {
	Hostname  string
	Port      int
	Username  string
	APIToken  string
	VerifySSL bool
}

// Client is a WHM API v1 client.
type Client struct {
	cfg     Config
	baseURL string
	http    *http.Client
}

// New creates a Client. Returns error if hostname is empty.
func New(cfg Config) (*Client, error) {
	if cfg.Hostname == "" {
		return nil, fmt.Errorf("cpanel: hostname is required")
	}
	if cfg.Port == 0 {
		cfg.Port = 2087
	}
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: !cfg.VerifySSL},
	}
	return &Client{
		cfg:     cfg,
		baseURL: fmt.Sprintf("https://%s:%d/json-api", cfg.Hostname, cfg.Port),
		http: &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
		},
	}, nil
}

// Account is one cPanel account as returned by listaccts.
type Account struct {
	Username      string
	PrimaryDomain string
	OwnerEmail    string
	Plan          string
	Suspended     bool
	DiskUsedMB    int
}

// Domain is one domain associated with a cPanel account.
type Domain struct {
	Domain string
	Type   string // "main" | "addon" | "parked" | "subdomain"
}

// Ping verifies connectivity and credentials by calling the version endpoint.
func (c *Client) Ping(ctx context.Context) error {
	var out struct {
		Metadata struct {
			Result  int    `json:"result"`
			Reason  string `json:"reason"`
			Version string `json:"version"`
		} `json:"metadata"`
	}
	if err := c.do(ctx, "GET", c.buildURL("version", nil), &out); err != nil {
		return fmt.Errorf("cpanel ping: %w", err)
	}
	if out.Metadata.Result != 1 {
		return fmt.Errorf("cpanel ping: %s", out.Metadata.Reason)
	}
	return nil
}

// ListAccounts calls GET /json-api/listaccts?api.version=1 and returns all accounts.
func (c *Client) ListAccounts(ctx context.Context) ([]Account, error) {
	params := url.Values{"api.version": {"1"}}
	var out struct {
		Metadata struct {
			Result int    `json:"result"`
			Reason string `json:"reason"`
		} `json:"metadata"`
		Data struct {
			Acct []struct {
				User      string `json:"user"`
				Domain    string `json:"domain"`
				Email     string `json:"email"`
				Plan      string `json:"plan"`
				Suspended int    `json:"suspended"`
				DiskMB    int    `json:"diskmb"`
			} `json:"acct"`
		} `json:"data"`
	}
	if err := c.do(ctx, "GET", c.buildURL("listaccts", params), &out); err != nil {
		return nil, fmt.Errorf("cpanel listaccts: %w", err)
	}
	if out.Metadata.Result != 1 {
		return nil, fmt.Errorf("cpanel listaccts: %s", out.Metadata.Reason)
	}
	accts := make([]Account, 0, len(out.Data.Acct))
	for _, a := range out.Data.Acct {
		accts = append(accts, Account{
			Username:      a.User,
			PrimaryDomain: a.Domain,
			OwnerEmail:    a.Email,
			Plan:          a.Plan,
			Suspended:     a.Suspended != 0,
			DiskUsedMB:    a.DiskMB,
		})
	}
	return accts, nil
}

// ListDomains returns all domains (main, addon, parked) for a given cPanel username.
func (c *Client) ListDomains(ctx context.Context, username, primaryDomain string) ([]Domain, error) {
	seen := map[string]bool{}
	domains := []Domain{}

	if primaryDomain != "" {
		d := strings.ToLower(primaryDomain)
		if !seen[d] {
			seen[d] = true
			domains = append(domains, Domain{Domain: d, Type: "main"})
		}
	}

	// Addon domains
	addonParams := url.Values{
		"api.version":               {"1"},
		"user":                      {username},
		"cpanel_jsonapi_module":     {"AddonDomain"},
		"cpanel_jsonapi_func":       {"listaddondomains"},
		"cpanel_jsonapi_apiversion": {"2"},
	}
	var addonOut struct {
		Result []struct {
			Domain string `json:"domain"`
		} `json:"result"`
		Status int `json:"status"`
	}
	if err := c.do(ctx, "GET", c.buildURL("cpanel", addonParams), &addonOut); err == nil && addonOut.Status == 1 {
		for _, a := range addonOut.Result {
			d := strings.ToLower(a.Domain)
			if d != "" && !seen[d] {
				seen[d] = true
				domains = append(domains, Domain{Domain: d, Type: "addon"})
			}
		}
	}

	// Parked/alias domains
	parkParams := url.Values{
		"api.version":               {"1"},
		"user":                      {username},
		"cpanel_jsonapi_module":     {"Park"},
		"cpanel_jsonapi_func":       {"listparkeddomains"},
		"cpanel_jsonapi_apiversion": {"2"},
	}
	var parkOut struct {
		Result struct {
			Domain []string `json:"domain"`
		} `json:"result"`
		Status int `json:"status"`
	}
	if err := c.do(ctx, "GET", c.buildURL("cpanel", parkParams), &parkOut); err == nil && parkOut.Status == 1 {
		for _, d := range parkOut.Result.Domain {
			dl := strings.ToLower(d)
			if dl != "" && !seen[dl] {
				seen[dl] = true
				domains = append(domains, Domain{Domain: dl, Type: "parked"})
			}
		}
	}

	return domains, nil
}

func (c *Client) buildURL(path string, params url.Values) string {
	u := c.baseURL + "/" + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	return u
}

func (c *Client) do(ctx context.Context, method, rawURL string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", fmt.Sprintf("whm %s:%s", c.cfg.Username, c.cfg.APIToken))
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("cpanel: authentication failed (status %d)", resp.StatusCode)
	}
	if resp.StatusCode >= 500 {
		return fmt.Errorf("cpanel: server error (status %d)", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
