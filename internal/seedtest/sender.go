package seedtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// TagHeader is the custom header stamped on every probe. Its value is the per-result probe
// tag, which the collector searches for to correlate a delivered message back to its seed.
const TagHeader = "X-MXS-Seed-Tag"

// RunTagHeader carries the run-level tag so an operator (or the collector) can see which run a
// probe belongs to at a glance.
const RunTagHeader = "X-MXS-Seed-Run"

// ProbeMessage is a fully-rendered probe ready to hand to a Sender.
type ProbeMessage struct {
	From   string
	To     string
	Tag    string // per-result probe tag (also in the X-MXS-Seed-Tag header)
	RunTag string
	Raw    []byte // complete RFC 5322 message (headers + synthetic body)
}

// Sender delivers a probe message. Implementations must not depend on package-level network
// state; the concrete SMTPSender takes an injectable send function so tests use a fake.
type Sender interface {
	Send(ctx context.Context, msg ProbeMessage) error
}

// SendFunc matches the signature of net/smtp.SendMail so the real transport can be swapped for
// a fake in tests.
type SendFunc func(addr string, a smtp.Auth, from string, to []string, msg []byte) error

// SMTPSender delivers probes through an SMTP submission endpoint (typically the relay itself,
// so placement reflects the production sending IP).
type SMTPSender struct {
	addr string    // host:port
	auth smtp.Auth // nil => no AUTH
	send SendFunc
}

// NewSMTPSender builds a Sender from SMTPConfig. When sendFn is nil it defaults to
// net/smtp.SendMail (which negotiates STARTTLS when the server advertises it). Passing a fake
// sendFn keeps tests offline.
func NewSMTPSender(cfg SMTPConfig, sendFn SendFunc) *SMTPSender {
	port := cfg.Port
	if port == 0 {
		port = DefaultSMTPPort
	}
	var auth smtp.Auth
	if strings.TrimSpace(cfg.Username) != "" {
		auth = smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
	}
	if sendFn == nil {
		sendFn = smtp.SendMail
	}
	return &SMTPSender{
		addr: net.JoinHostPort(cfg.Host, fmt.Sprintf("%d", port)),
		auth: auth,
		send: sendFn,
	}
}

// Send delivers the probe. A blank recipient or sender is rejected before dialing.
func (s *SMTPSender) Send(ctx context.Context, msg ProbeMessage) error {
	if strings.TrimSpace(msg.To) == "" {
		return fmt.Errorf("seedtest: probe has no recipient")
	}
	if strings.TrimSpace(msg.From) == "" {
		return fmt.Errorf("seedtest: probe has no sender")
	}
	// net/smtp.SendMail is not context-aware; honor cancellation before we start.
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.send(s.addr, s.auth, msg.From, []string{msg.To}, msg.Raw)
}

// NewProbeTag returns a short, URL/address-safe unique token used to correlate a probe with
// its seed result. It is hex-encoded random bytes, so it never contains characters that would
// break a plus-address or header value.
func NewProbeTag() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is fatal for uniqueness; fall back to a time-based tag.
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// PlusAddress injects a tag into the local part of an address using the "+" sub-address
// convention (user+tag@domain). If the address has no "@", the tag is appended with "+". This
// gives providers that honor plus-addressing a second correlation signal beyond the header.
func PlusAddress(address, tag string) string {
	at := strings.LastIndex(address, "@")
	if at < 0 {
		return address + "+" + tag
	}
	local, domain := address[:at], address[at+1:]
	// Don't double-tag if the local part already carries a plus segment.
	if i := strings.IndexByte(local, '+'); i >= 0 {
		local = local[:i]
	}
	return local + "+" + tag + "@" + domain
}

// BuildProbe renders a complete probe message for a seed. from is the sender address, to is
// the seed mailbox, runTag/tag are the correlation tokens. The recipient in the To header uses
// the plus-addressed form; the envelope recipient the caller sends to should be the bare seed
// address (some providers reject plus-addresses at the MAIL step) — the daemon handles that.
//
// The body is deliberately synthetic, benign boilerplate — never real user content — so it is
// safe to transmit and to read back.
func BuildProbe(from, to, runTag, tag string, now time.Time) ProbeMessage {
	msgID := fmt.Sprintf("<%s.%s@mxsentinel.seedtest>", tag, runTag)
	subject := fmt.Sprintf("MX Sentinel inbox placement probe [%s]", tag)
	var b strings.Builder
	writeHeader(&b, "From", from)
	writeHeader(&b, "To", PlusAddress(to, tag))
	writeHeader(&b, "Subject", subject)
	writeHeader(&b, "Date", now.UTC().Format(time.RFC1123Z))
	writeHeader(&b, "Message-ID", msgID)
	writeHeader(&b, RunTagHeader, runTag)
	writeHeader(&b, TagHeader, tag)
	writeHeader(&b, "MIME-Version", "1.0")
	writeHeader(&b, "Content-Type", "text/plain; charset=utf-8")
	b.WriteString("\r\n")
	b.WriteString("This is an automated deliverability probe generated by MX Sentinel.\r\n")
	b.WriteString("It contains no personal data. You may safely ignore or delete it.\r\n")
	b.WriteString("\r\n")
	b.WriteString("Probe tag: " + tag + "\r\n")
	b.WriteString("Run tag:   " + runTag + "\r\n")

	return ProbeMessage{
		From:   from,
		To:     to,
		Tag:    tag,
		RunTag: runTag,
		Raw:    []byte(b.String()),
	}
}

func writeHeader(b *strings.Builder, key, val string) {
	// collapse CR/LF in values to prevent header injection from anything caller-supplied.
	val = strings.ReplaceAll(val, "\r", " ")
	val = strings.ReplaceAll(val, "\n", " ")
	b.WriteString(key)
	b.WriteString(": ")
	b.WriteString(val)
	b.WriteString("\r\n")
}
