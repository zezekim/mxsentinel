package mtasts

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"time"
)

// maxPolicyBytes caps the policy body we read (RFC 8461 policies are tiny; this guards
// against a hostile endpoint streaming megabytes).
const maxPolicyBytes = 64 * 1024

// HTTPPolicyFetcher fetches https://mta-sts.<domain>/.well-known/mta-sts.txt.
type HTTPPolicyFetcher struct {
	client *http.Client
}

// NewHTTPPolicyFetcher returns a fetcher with the given per-request timeout (default 10s).
// Redirects are refused per RFC 8461 §3.3.
func NewHTTPPolicyFetcher(timeout time.Duration) *HTTPPolicyFetcher {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &HTTPPolicyFetcher{client: &http.Client{
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

// Fetch retrieves and returns the policy body.
func (f *HTTPPolicyFetcher) Fetch(ctx context.Context, domain string) (string, error) {
	url := "https://mta-sts." + domain + "/.well-known/mta-sts.txt"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("policy endpoint returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPolicyBytes))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// SMTPCertChecker retrieves an MX host's TLS certificate via SMTP STARTTLS on port 25.
type SMTPCertChecker struct {
	timeout time.Duration
}

// NewSMTPCertChecker returns a checker with the given dial/handshake timeout (default 10s).
func NewSMTPCertChecker(timeout time.Duration) *SMTPCertChecker {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &SMTPCertChecker{timeout: timeout}
}

// Check dials host:25, negotiates STARTTLS, and inspects the presented leaf certificate.
// Any dial/handshake failure is returned in CertInfo.Err rather than panicking so one bad
// host never stops an inspection.
func (c *SMTPCertChecker) Check(ctx context.Context, host string) CertInfo {
	info := CertInfo{Host: host}
	dialer := net.Dialer{Timeout: c.timeout}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, "25"))
	if err != nil {
		info.Err = err.Error()
		return info
	}
	_ = conn.SetDeadline(time.Now().Add(c.timeout))

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		info.Err = err.Error()
		return info
	}
	defer client.Close()

	if err := client.Hello("mxsentinel"); err != nil {
		info.Err = err.Error()
		return info
	}
	// Validate the chain against the MX hostname (ServerName), not InsecureSkipVerify.
	if err := client.StartTLS(&tls.Config{ServerName: host}); err != nil {
		// A verification failure here means the cert is present but invalid for the host.
		info.Err = err.Error()
		return info
	}
	cs, ok := client.TLSConnectionState()
	if !ok || len(cs.PeerCertificates) == 0 {
		info.Err = "no certificate presented"
		return info
	}
	leaf := cs.PeerCertificates[0]
	info.Valid = true
	info.NotAfter = leaf.NotAfter
	return info
}

// Config holds tlsrptd's feature-specific runtime knobs, resolved from environment
// variables with safe defaults (precedent: internal/rbl.LoadConfig). Infrastructure config
// (Postgres/ClickHouse/NATS/object store) still comes from internal/config.Load.
type Config struct {
	DropDir      string        // MXS_TLSRPT_DIR — TLS-RPT report drop directory
	PollInterval time.Duration // MXS_MTASTS_INTERVAL — MTA-STS re-check interval
	DropInterval time.Duration // MXS_TLSRPT_INTERVAL — drop-dir scan interval
	HTTPTimeout  time.Duration // MXS_MTASTS_HTTP_TIMEOUT
	CertTimeout  time.Duration // MXS_MTASTS_CERT_TIMEOUT
	CertWarnDays int           // MXS_MTASTS_CERT_WARN_DAYS
}

// LoadConfig reads tlsrptd's configuration from the environment.
func LoadConfig() Config {
	return Config{
		DropDir:      getenv("MXS_TLSRPT_DIR", ""),
		PollInterval: parseDuration("MXS_MTASTS_INTERVAL", time.Hour),
		DropInterval: parseDuration("MXS_TLSRPT_INTERVAL", 30*time.Second),
		HTTPTimeout:  parseDuration("MXS_MTASTS_HTTP_TIMEOUT", 10*time.Second),
		CertTimeout:  parseDuration("MXS_MTASTS_CERT_TIMEOUT", 10*time.Second),
		CertWarnDays: parseInt("MXS_MTASTS_CERT_WARN_DAYS", 14),
	}
}

func getenv(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func parseDuration(key string, def time.Duration) time.Duration {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return def
}

func parseInt(key string, def int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}
