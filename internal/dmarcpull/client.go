// Package dmarcpull pulls DMARC aggregate report data from an external DMARC
// receiver API and feeds it into MX Sentinel's Postgres + ClickHouse stores.
package dmarcpull

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Client is an HTTP client for the external DMARC receiver pull API.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// NewClient constructs a Client. baseURL should be the full base path,
// e.g. "https://dmarc.example.com/api/v1".
func NewClient(baseURL, apiKey string) *Client {
	return &Client{
		baseURL: baseURL,
		apiKey:  apiKey,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// Ping verifies the API is reachable and the key is accepted.
func (c *Client) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("dmarc pull healthz: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("dmarc pull healthz: unexpected status %d", resp.StatusCode)
	}
	return nil
}

// RemoteReport is one item from GET /api/v1/reports.
type RemoteReport struct {
	ID          string    `json:"id"`
	OrgName     string    `json:"org_name"`
	ReportID    string    `json:"report_id"`
	Domain      string    `json:"domain"`
	DateBegin   time.Time `json:"date_begin"`
	DateEnd     time.Time `json:"date_end"`
	RecordCount int       `json:"record_count"`
	OrgEmail    string    `json:"org_email"`
	IngestedAt  time.Time `json:"ingested_at"`
}

// ReportsPage is the response from GET /api/v1/reports.
type ReportsPage struct {
	Reports    []RemoteReport `json:"reports"`
	NextCursor string         `json:"next_cursor"`
}

// ListReports fetches one page of reports. Pass since=zero to fetch from the
// beginning. Pass cursor="" for the first page; pass the value from the previous
// page's NextCursor for subsequent pages.
func (c *Client) ListReports(ctx context.Context, since time.Time, cursor string, limit int) (ReportsPage, error) {
	params := url.Values{}
	if !since.IsZero() {
		params.Set("since", since.UTC().Format(time.RFC3339))
	}
	if cursor != "" {
		params.Set("cursor", cursor)
	}
	if limit > 0 {
		params.Set("limit", fmt.Sprintf("%d", limit))
	}
	u := c.baseURL + "/reports"
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	var page ReportsPage
	if err := c.getJSON(ctx, u, &page); err != nil {
		return ReportsPage{}, fmt.Errorf("list reports: %w", err)
	}
	return page, nil
}

// RemoteRecord is one per-source row from GET /api/v1/reports/{id}/records.
type RemoteRecord struct {
	SourceIP     string `json:"source_ip"`
	Count        uint32 `json:"count"`
	Disposition  string `json:"disposition"`
	DKIMAligned  bool   `json:"dkim_aligned"`
	SPFAligned   bool   `json:"spf_aligned"`
	HeaderFrom   string `json:"header_from"`
	EnvelopeFrom string `json:"envelope_from"`
}

// RecordsResponse is the response from GET /api/v1/reports/{id}/records.
type RecordsResponse struct {
	ReportID  string         `json:"report_id"`
	OrgName   string         `json:"org_name"`
	Domain    string         `json:"domain"`
	DateBegin time.Time      `json:"date_begin"`
	DateEnd   time.Time      `json:"date_end"`
	Records   []RemoteRecord `json:"records"`
}

// GetRecords fetches all per-source records for a report by its remote ID.
func (c *Client) GetRecords(ctx context.Context, reportID string) (RecordsResponse, error) {
	u := fmt.Sprintf("%s/reports/%s/records", c.baseURL, url.PathEscape(reportID))
	var resp RecordsResponse
	if err := c.getJSON(ctx, u, &resp); err != nil {
		return RecordsResponse{}, fmt.Errorf("get records for %s: %w", reportID, err)
	}
	return resp, nil
}

func (c *Client) getJSON(ctx context.Context, u string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20)) // 8 MiB
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
