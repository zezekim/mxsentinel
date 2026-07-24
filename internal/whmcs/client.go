package whmcs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Config holds connection parameters for one WHMCS installation.
type Config struct {
	APIURL        string
	APIIdentifier string
	APISecret     string
}

// Client is a WHMCS API client.
type Client struct {
	cfg  Config
	http *http.Client
}

// New creates a Client. Returns error if APIURL is empty.
func New(cfg Config) (*Client, error) {
	if cfg.APIURL == "" {
		return nil, fmt.Errorf("whmcs: api_url is required")
	}
	return &Client{
		cfg:  cfg,
		http: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// ClientRecord maps to a WHMCS client.
type ClientRecord struct {
	ID          int    `json:"id"`
	FirstName   string `json:"firstname"`
	LastName    string `json:"lastname"`
	Email       string `json:"email"`
	CompanyName string `json:"companyname"`
	Status      string `json:"status"`
}

// AccountMetrics holds email metrics for one billing period for one cPanel account.
type AccountMetrics struct {
	CpanelUsername string
	PrimaryDomain  string
	OwnerEmail     string
	Delivered      int64
	Bounced        int64
	Rejected       int64
	Deferred       int64
	Total          int64
	PeriodStart    time.Time
	PeriodEnd      time.Time
	// ReportText, when set, is a full deliverability report block added to the WHMCS client as
	// a note (in addition to the billable item) — a copy-paste-ready per-client summary.
	ReportText string
}

// Ping verifies credentials by calling GetClients with limit=1.
func (c *Client) Ping(ctx context.Context) error {
	params := url.Values{"limitnum": {"1"}}
	resp, err := c.post(ctx, "GetClients", params)
	if err != nil {
		return fmt.Errorf("whmcs ping: %w", err)
	}
	if r, ok := resp["result"].(string); ok && r != "success" {
		msg, _ := resp["message"].(string)
		return fmt.Errorf("whmcs ping: %s", msg)
	}
	return nil
}

// GetClientByEmail looks up a WHMCS client by email address.
// Returns nil (no error) if not found.
func (c *Client) GetClientByEmail(ctx context.Context, email string) (*ClientRecord, error) {
	if email == "" {
		return nil, nil
	}
	params := url.Values{"search": {email}, "limitnum": {"1"}}
	resp, err := c.post(ctx, "GetClients", params)
	if err != nil {
		return nil, fmt.Errorf("whmcs GetClientByEmail: %w", err)
	}
	clients, ok := resp["clients"].(map[string]interface{})
	if !ok {
		return nil, nil
	}
	clientList, ok := clients["client"].([]interface{})
	if !ok || len(clientList) == 0 {
		return nil, nil
	}
	raw, _ := json.Marshal(clientList[0])
	var cr ClientRecord
	if err := json.Unmarshal(raw, &cr); err != nil {
		return nil, err
	}
	if !strings.EqualFold(cr.Email, email) {
		return nil, nil
	}
	return &cr, nil
}

// GetClientByDomain looks up the WHMCS client that owns a given domain.
// Returns nil (no error) if not found.
func (c *Client) GetClientByDomain(ctx context.Context, domain string) (*ClientRecord, error) {
	if domain == "" {
		return nil, nil
	}
	params := url.Values{"domain": {domain}, "limitnum": {"1"}}
	resp, err := c.post(ctx, "GetClientsDomains", params)
	if err != nil {
		return nil, fmt.Errorf("whmcs GetClientByDomain: %w", err)
	}
	domains, ok := resp["domains"].(map[string]interface{})
	if !ok {
		return nil, nil
	}
	domainList, ok := domains["domain"].([]interface{})
	if !ok || len(domainList) == 0 {
		return nil, nil
	}
	firstDomain, ok := domainList[0].(map[string]interface{})
	if !ok {
		return nil, nil
	}
	clientIDRaw, ok := firstDomain["userid"]
	if !ok {
		return nil, nil
	}
	var clientID int
	switch v := clientIDRaw.(type) {
	case float64:
		clientID = int(v)
	case string:
		_, _ = fmt.Sscanf(v, "%d", &clientID)
	}
	if clientID == 0 {
		return nil, nil
	}
	clientParams := url.Values{"clientid": {fmt.Sprintf("%d", clientID)}}
	clientResp, err := c.post(ctx, "GetClientsDetails", clientParams)
	if err != nil {
		return nil, fmt.Errorf("whmcs GetClientsDetails: %w", err)
	}
	raw, _ := json.Marshal(clientResp)
	var cr ClientRecord
	if err := json.Unmarshal(raw, &cr); err != nil {
		return nil, err
	}
	return &cr, nil
}

// PushMetrics creates a zero-amount billable item on the WHMCS client record
// for each account, recording the email metrics summary for the period.
// Returns the number of clients successfully updated.
func (c *Client) PushMetrics(ctx context.Context, metrics []AccountMetrics) (int, error) {
	updated := 0
	var lastErr error

	for _, m := range metrics {
		if m.Delivered == 0 && m.Bounced == 0 && m.Rejected == 0 && m.Total == 0 {
			continue
		}

		// Try to find the WHMCS client by owner email first, then by primary domain.
		var client *ClientRecord
		var err error

		if m.OwnerEmail != "" {
			client, err = c.GetClientByEmail(ctx, m.OwnerEmail)
			if err != nil {
				lastErr = err
				continue
			}
		}
		if client == nil && m.PrimaryDomain != "" {
			client, err = c.GetClientByDomain(ctx, m.PrimaryDomain)
			if err != nil {
				lastErr = err
				continue
			}
		}
		if client == nil {
			continue
		}

		period := fmt.Sprintf("%s – %s",
			m.PeriodStart.UTC().Format("2006-01-02"),
			m.PeriodEnd.UTC().Format("2006-01-02"),
		)
		description := fmt.Sprintf(
			"MX Sentinel Email Report %s | %s | Delivered=%d Bounced=%d Rejected=%d Total=%d",
			period, m.CpanelUsername, m.Delivered, m.Bounced, m.Rejected, m.Total,
		)

		params := url.Values{
			"clientid":      {fmt.Sprintf("%d", client.ID)},
			"description":   {description},
			"amount":        {"0.00"},
			"invoiceaction": {"noinvoice"},
		}
		resp, err := c.post(ctx, "AddBillableItem", params)
		if err != nil {
			lastErr = err
			continue
		}
		if r, ok := resp["result"].(string); ok && r == "success" {
			updated++
		}

		// Attach the full deliverability report as a client note (copy-paste-ready). Best-effort:
		// a note failure must not abort the whole push.
		if m.ReportText != "" {
			if nerr := c.AddClientNote(ctx, client.ID, m.ReportText); nerr != nil {
				lastErr = nerr
			}
		}
	}

	return updated, lastErr
}

// AddClientNote appends a note to a WHMCS client record (the "Notes" tab). Used to attach the
// per-period deliverability report block so it's copy-paste-ready from inside WHMCS.
func (c *Client) AddClientNote(ctx context.Context, clientID int, note string) error {
	params := url.Values{
		"userid": {fmt.Sprintf("%d", clientID)},
		"notes":  {note},
		"sticky": {"false"},
	}
	resp, err := c.post(ctx, "AddClientNote", params)
	if err != nil {
		return err
	}
	if r, ok := resp["result"].(string); ok && r != "success" {
		if msg, _ := resp["message"].(string); msg != "" {
			return fmt.Errorf("AddClientNote: %s", msg)
		}
		return fmt.Errorf("AddClientNote: unexpected result %q", r)
	}
	return nil
}

// post sends a POST request to the WHMCS API and returns the decoded response.
func (c *Client) post(ctx context.Context, action string, params url.Values) (map[string]interface{}, error) {
	form := url.Values{
		"identifier":   {c.cfg.APIIdentifier},
		"secret":       {c.cfg.APISecret},
		"action":       {action},
		"responsetype": {"json"},
	}
	for k, vs := range params {
		form[k] = vs
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.APIURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("whmcs %s: %w", action, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("whmcs %s: HTTP %d", action, resp.StatusCode)
	}

	var out map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("whmcs %s: decode response: %w", action, err)
	}
	if r, ok := out["result"].(string); ok && r == "error" {
		msg, _ := out["message"].(string)
		return nil, fmt.Errorf("whmcs %s: %s", action, msg)
	}
	return out, nil
}
