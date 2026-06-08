package fbl

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// postmasterBaseURL is the Gmail Postmaster Tools v1 REST endpoint. The traffic stats
// resource carries domain reputation and the user-reported spam ratio.
const postmasterBaseURL = "https://gmailpostmastertools.googleapis.com/v1"

// PostmasterClient pulls per-domain reputation from Gmail Postmaster Tools using a bearer
// token the operator supplies/refreshes out-of-band (env GOOGLE_POSTMASTER_TOKEN). We do
// NOT do the OAuth dance here — no oauth2 module is in the build — the operator mints an
// access token (gcloud / a sidecar) and rotates it; an expired token surfaces as a 401
// that is logged and skipped, never fabricated.
type PostmasterClient struct {
	token string
	hc    *http.Client
}

// NewPostmasterClient returns a client, or ok=false when no token is configured (caller
// then skips the Postmaster half cleanly).
func NewPostmasterClient(token string) (*PostmasterClient, bool) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, false
	}
	return &PostmasterClient{
		token: token,
		hc:    &http.Client{Timeout: 20 * time.Second},
	}, true
}

// DomainReport is the normalized reputation for one domain.
type DomainReport struct {
	Domain     string
	Reputation string  // HIGH | GOOD | MEDIUM | LOW | BAD
	SpamRate   float64 // user-reported spam ratio (0..1)
	Date       string  // YYYY-MM-DD of the stats row used
}

// trafficStats mirrors the subset of the Postmaster trafficStats resource we read.
// See gmailpostmastertools.domains.trafficStats. Fields absent in a row stay zero/"".
type trafficStats struct {
	Name                  string  `json:"name"`
	DomainReputation      string  `json:"domainReputation"`
	UserReportedSpamRatio float64 `json:"userReportedSpamRatio"`
	SpammyFeedbackLoops   []any   `json:"spammyFeedbackLoops"`
}

type listTrafficStatsResp struct {
	TrafficStats []trafficStats `json:"trafficStats"`
}

type listDomainsResp struct {
	Domains []struct {
		Name string `json:"name"` // "domains/example.com"
	} `json:"domains"`
}

// ListDomains returns the domains verified in this Postmaster account (their bare names).
func (c *PostmasterClient) ListDomains(ctx context.Context) ([]string, error) {
	var resp listDomainsResp
	if err := c.getJSON(ctx, postmasterBaseURL+"/domains", &resp); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(resp.Domains))
	for _, d := range resp.Domains {
		out = append(out, strings.TrimPrefix(d.Name, "domains/"))
	}
	return out, nil
}

// FetchDomain returns the most recent trafficStats reputation for one domain. ok=false
// (nil error) when Postmaster has no stats yet (a freshly-verified or low-volume domain).
func (c *PostmasterClient) FetchDomain(ctx context.Context, domain string) (DomainReport, bool, error) {
	// trafficStats are keyed by date; list the available rows and take the newest. The API
	// returns up to 30 days; we don't page because we only need the latest grade.
	u := fmt.Sprintf("%s/domains/%s/trafficStats", postmasterBaseURL, url.PathEscape(domain))
	var resp listTrafficStatsResp
	if err := c.getJSON(ctx, u, &resp); err != nil {
		return DomainReport{}, false, err
	}
	if len(resp.TrafficStats) == 0 {
		return DomainReport{}, false, nil
	}
	// trafficStats names embed the date: domains/<d>/trafficStats/YYYYMMDD. Pick the max.
	best := resp.TrafficStats[0]
	bestDate := dateOfStatName(best.Name)
	for _, ts := range resp.TrafficStats[1:] {
		if d := dateOfStatName(ts.Name); d > bestDate {
			best, bestDate = ts, d
		}
	}
	rep := normalizeReputation(best.DomainReputation)
	return DomainReport{
		Domain:     domain,
		Reputation: rep,
		SpamRate:   best.UserReportedSpamRatio,
		Date:       bestDate,
	}, rep != "" || best.UserReportedSpamRatio > 0, nil
}

func (c *PostmasterClient) getJSON(ctx context.Context, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("postmaster request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("postmaster api %s: status %d", u, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return fmt.Errorf("decode postmaster response: %w", err)
	}
	return nil
}

// normalizeReputation maps the Postmaster enum (REPUTATION_CATEGORY_UNSPECIFIED, HIGH,
// MEDIUM, LOW, BAD) to the short grades the dashboard badges on. GOOD is a Postmaster
// alias some responses use; we keep it.
func normalizeReputation(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	switch s {
	case "HIGH", "GOOD", "MEDIUM", "LOW", "BAD":
		return s
	default:
		return "" // unspecified / unknown -> store empty (UI shows "unknown")
	}
}

// IsBadReputation reports whether a grade should trip a critical incident.
func IsBadReputation(rep string) bool {
	switch strings.ToUpper(strings.TrimSpace(rep)) {
	case "LOW", "BAD":
		return true
	default:
		return false
	}
}

func dateOfStatName(name string) string {
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		return name[i+1:]
	}
	return name
}
