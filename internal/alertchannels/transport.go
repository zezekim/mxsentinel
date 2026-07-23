package alertchannels

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/smtp"
	"strings"
	"time"
)

// httpDoer is the production HTTPDoer, backed by net/http. It treats any non-2xx response
// as an error so the dispatcher records a failed delivery.
type httpDoer struct {
	client *http.Client
}

// NewHTTPDoer returns a production HTTPDoer with the given per-request timeout.
func NewHTTPDoer(timeout time.Duration) HTTPDoer {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &httpDoer{client: &http.Client{Timeout: timeout}}
}

func (h *httpDoer) Do(ctx context.Context, req *HTTPRequest) error {
	hr, err := http.NewRequestWithContext(ctx, req.Method, req.URL, bytes.NewReader(req.Body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	for k, v := range req.Header {
		hr.Header.Set(k, v)
	}
	resp, err := h.client.Do(hr)
	if err != nil {
		return fmt.Errorf("send: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	// Drain so the connection can be reused.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return nil
}

// smtpMailer is the production Mailer, backed by net/smtp. When Username is empty it sends
// without authentication (e.g. a local relay / MX Sentinel's own relay).
type smtpMailer struct {
	host     string // host:port
	username string
	password string
}

// NewSMTPMailer returns a production Mailer. addr is "host:port".
func NewSMTPMailer(addr, username, password string) Mailer {
	return &smtpMailer{host: addr, username: username, password: password}
}

func (m *smtpMailer) Send(ctx context.Context, msg *EmailMessage) error {
	if m.host == "" {
		return fmt.Errorf("email: no SMTP host configured")
	}
	raw := renderRFC822(msg)

	var auth smtp.Auth
	if m.username != "" {
		hostOnly := m.host
		if i := strings.LastIndex(m.host, ":"); i >= 0 {
			hostOnly = m.host[:i]
		}
		auth = smtp.PlainAuth("", m.username, m.password, hostOnly)
	}

	done := make(chan error, 1)
	go func() {
		done <- smtp.SendMail(m.host, auth, msg.From, msg.To, raw)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		if err != nil {
			return fmt.Errorf("smtp send: %w", err)
		}
		return nil
	}
}

// renderRFC822 builds a minimal RFC 822 plain-text message. Pure helper (no I/O).
func renderRFC822(msg *EmailMessage) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", msg.From)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(msg.To, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", msg.Subject)
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=\"utf-8\"\r\n")
	b.WriteString("\r\n")
	b.WriteString(strings.ReplaceAll(msg.Body, "\n", "\r\n"))
	return []byte(b.String())
}
