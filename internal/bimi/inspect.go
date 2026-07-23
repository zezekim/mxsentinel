package bimi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Resolver is the minimal DNS surface BIMI needs. internal/dns.Resolver (SystemResolver,
// StaticResolver) satisfies it structurally, so cmd/bimid reuses the same resolver dnsd uses.
type Resolver interface {
	TXT(ctx context.Context, name string) ([]string, error)
}

// Fetcher retrieves the logo/VMC over HTTP. It is injectable so tests never touch the network.
type Fetcher interface {
	Fetch(ctx context.Context, url string) ([]byte, error)
}

// LookupRecord resolves the default._bimi.<domain> TXT record and returns the raw v=BIMI1
// string (or "" when none is published). A lookup error is returned so the caller can decide
// whether to treat it as "not configured" or a transient failure.
func LookupRecord(ctx context.Context, r Resolver, domain string) (string, error) {
	name := "default._bimi." + strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
	txts, err := r.TXT(ctx, name)
	if err != nil {
		return "", err
	}
	for _, t := range txts {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(t)), "v=bimi1") {
			return t, nil
		}
	}
	return "", nil
}

// Inspect performs a full live assessment of a domain: it resolves the BIMI record, fetches
// the referenced logo and VMC (when present), and assembles the Report. dmarcRecord is the
// domain's existing DMARC TXT (from the latest DNS snapshot) — BIMI reuses it rather than
// re-resolving DMARC. Fetch failures become checklist failures, never hard errors.
func Inspect(ctx context.Context, r Resolver, f Fetcher, domain, dmarcRecord string) (Report, error) {
	recordTXT, err := LookupRecord(ctx, r, domain)
	if err != nil {
		return Report{}, fmt.Errorf("resolve BIMI record for %s: %w", domain, err)
	}

	rec := ParseRecord(recordTXT)

	var logo, vmc *Artifact
	if rec.Valid {
		if rec.LogoURL != "" {
			logo = fetchArtifact(ctx, f, rec.LogoURL)
		}
		if rec.VMCURL != "" {
			vmc = fetchArtifact(ctx, f, rec.VMCURL)
		}
	}

	return Assess(domain, recordTXT, dmarcRecord, logo, vmc, time.Now()), nil
}

func fetchArtifact(ctx context.Context, f Fetcher, url string) *Artifact {
	body, err := f.Fetch(ctx, url)
	if err != nil {
		return &Artifact{Fetched: true, Err: err.Error()}
	}
	return &Artifact{Fetched: true, Body: body}
}

// HTTPFetcher is the production Fetcher: a size-capped, timeout-bounded HTTP GET. Only http/https
// URLs are honored, and responses are truncated to maxFetchBytes.
type HTTPFetcher struct {
	Client        *http.Client
	MaxFetchBytes int64
}

// NewHTTPFetcher returns an HTTPFetcher with a per-request timeout and a 256 KiB read cap.
func NewHTTPFetcher(timeout time.Duration) *HTTPFetcher {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &HTTPFetcher{
		Client:        &http.Client{Timeout: timeout},
		MaxFetchBytes: 256 * 1024,
	}
}

func (h *HTTPFetcher) Fetch(ctx context.Context, url string) ([]byte, error) {
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("unsupported URL scheme: %s", url)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "mxsentinel-bimid/1.0")
	resp, err := h.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	capBytes := h.MaxFetchBytes
	if capBytes <= 0 {
		capBytes = 256 * 1024
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, capBytes))
	if err != nil {
		return nil, err
	}
	return body, nil
}
