package telemetry

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zezekim/mxsentinel/pkg/contracts"
)

func newParser() *Parser { return NewParser(2026, "198.51.100.5", []byte("test-key")) }

func feed(p *Parser, lines ...string) []Event {
	var out []Event
	for _, l := range lines {
		out = append(out, p.Parse(l)...)
	}
	return out
}

func TestParseDeliveredSequence(t *testing.T) {
	p := newParser()
	evs := feed(p,
		"Jun  3 10:00:00 mail postfix/cleanup[1230]: 4F1AB2C3: message-id=<abc123@example.com>",
		"Jun  3 10:00:00 mail postfix/qmgr[1234]: 4F1AB2C3: from=<bounce@example.com>, size=2048, nrcpt=1 (queue active)",
		"Jun  3 10:00:01 mail postfix/smtp[1240]: 4F1AB2C3: to=<user@gmail.com>, relay=gmail-smtp-in.l.google.com[142.250.1.2]:25, delay=1.2, delays=0.1/0/0.5/0.6, dsn=2.0.0, status=sent (250 2.0.0 OK 1700000000 a1-gsmtp)",
		"Jun  3 10:00:01 mail postfix/qmgr[1234]: 4F1AB2C3: removed",
	)
	if len(evs) != 1 {
		t.Fatalf("expected 1 event, got %d", len(evs))
	}
	ev := evs[0]
	p2 := ev.Payload

	checks := map[string]struct{ got, want string }{
		"type":          {string(ev.Type), string(contracts.EventSMTPDelivered)},
		"outcome":       {p2.Outcome, "delivered"},
		"bounce_class":  {p2.BounceClass, "none"},
		"message_id":    {p2.MessageID, "<abc123@example.com>"},
		"queue_id":      {p2.QueueID, "4F1AB2C3"},
		"envelope_from": {p2.EnvelopeFrom, "bounce@example.com"},
		"from_domain":   {p2.FromDomain, "example.com"},
		"rcpt_domain":   {p2.RecipientDomain, "gmail.com"},
		"provider":      {p2.Provider, "google"},
		"mx_host":       {p2.MXHost, "gmail-smtp-in.l.google.com"},
		"enhanced":      {p2.EnhancedStatus, "2.0.0"},
		"relay_ip":      {p2.RelayIP, "198.51.100.5"},
	}
	for name, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", name, c.got, c.want)
		}
	}
	if p2.SMTPCode != 250 {
		t.Errorf("smtp_code = %d, want 250", p2.SMTPCode)
	}
	if p2.SizeBytes != 2048 {
		t.Errorf("size_bytes = %d, want 2048", p2.SizeBytes)
	}
	if p2.RecipientHash == "" || p2.RecipientHash == "user@gmail.com" {
		t.Errorf("recipient hash not applied: %q", p2.RecipientHash)
	}

	// Privacy: the full recipient address must not appear anywhere in the event.
	blob, _ := json.Marshal(ev)
	if strings.Contains(string(blob), "user@gmail.com") {
		t.Errorf("recipient address leaked into event: %s", blob)
	}
}

func TestParseCapturesSASLUsername(t *testing.T) {
	p := newParser()
	// The submission smtpd line (authenticated client) carries sasl_username; it must be
	// attributed to the later delivery event via the shared queue id.
	evs := feed(p,
		"Jun  3 10:00:00 mail postfix/submission[1200]: 7A1BC2D3: client=cpanel.example.com[203.0.113.9], sasl_method=LOGIN, sasl_username=mailer@send.example.com",
		"Jun  3 10:00:00 mail postfix/cleanup[1230]: 7A1BC2D3: message-id=<x1@send.example.com>",
		"Jun  3 10:00:00 mail postfix/qmgr[1234]: 7A1BC2D3: from=<mailer@send.example.com>, size=900, nrcpt=1 (queue active)",
		"Jun  3 10:00:01 mail postfix/smtp[1240]: 7A1BC2D3: to=<dest@gmail.com>, relay=gmail-smtp-in.l.google.com[142.250.1.2]:25, dsn=2.0.0, status=sent (250 2.0.0 OK)",
	)
	if len(evs) != 1 {
		t.Fatalf("expected 1 event, got %d", len(evs))
	}
	if got := evs[0].Payload.SASLUsername; got != "mailer@send.example.com" {
		t.Errorf("sasl_username = %q, want %q", got, "mailer@send.example.com")
	}
}

func TestParseBouncedHard(t *testing.T) {
	p := newParser()
	evs := feed(p,
		"Jun  3 10:05:00 mail postfix/smtp[1250]: 9A8B7C6D: to=<nobody@example.org>, relay=mx.example.org[203.0.113.9]:25, dsn=5.1.1, status=bounced (host mx.example.org said: 550 5.1.1 User unknown)",
	)
	if len(evs) != 1 {
		t.Fatalf("expected 1 event, got %d", len(evs))
	}
	if evs[0].Type != contracts.EventSMTPBounced {
		t.Errorf("type = %q, want bounced", evs[0].Type)
	}
	if evs[0].Payload.BounceClass != "hard" {
		t.Errorf("bounce_class = %q, want hard", evs[0].Payload.BounceClass)
	}
	if evs[0].Payload.SMTPCode != 550 {
		t.Errorf("smtp_code = %d, want 550", evs[0].Payload.SMTPCode)
	}
}

func TestParseDeferredBlock(t *testing.T) {
	p := newParser()
	evs := feed(p,
		"Jun  3 10:06:00 mail postfix/smtp[1260]: AB12CD34: to=<x@outlook.com>, relay=outlook.com[1.2.3.4]:25, dsn=4.7.0, status=deferred (host outlook.com said: 421 4.7.0 message blocked due to reputation)",
	)
	if len(evs) != 1 {
		t.Fatalf("expected 1 event, got %d", len(evs))
	}
	ev := evs[0]
	if ev.Type != contracts.EventSMTPDeferred {
		t.Errorf("type = %q, want deferred", ev.Type)
	}
	if ev.Payload.BounceClass != "block" {
		t.Errorf("bounce_class = %q, want block", ev.Payload.BounceClass)
	}
	if ev.Payload.Provider != "microsoft" {
		t.Errorf("provider = %q, want microsoft", ev.Payload.Provider)
	}
}

func TestParseIgnoresNonDelivery(t *testing.T) {
	p := newParser()
	if evs := feed(p, "Jun  3 10:00:00 mail postfix/smtpd[99]: NOQUEUE: reject: RCPT from ..."); len(evs) != 0 {
		t.Errorf("NOQUEUE line should produce no events, got %d", len(evs))
	}
	if evs := feed(p, "Jun  3 10:00:00 mail systemd[1]: started something"); len(evs) != 0 {
		t.Errorf("non-postfix line should produce no events")
	}
}

func TestClassifyProvider(t *testing.T) {
	cases := map[string]string{
		"gmail-smtp-in.l.google.com":              "google",
		"example-com.mail.protection.outlook.com": "microsoft",
		"mta5.am0.yahoodns.net":                   "yahoo",
		"mx01.mail.icloud.com":                    "apple",
		"mail.randomhost.tld":                     "other",
		"":                                        "",
	}
	for host, want := range cases {
		if got := classifyProvider(host); got != want {
			t.Errorf("classifyProvider(%q) = %q, want %q", host, got, want)
		}
	}
}

func TestPrivacyHelpers(t *testing.T) {
	if domainOf("a@b.com") != "b.com" {
		t.Error("domainOf failed")
	}
	if domainOf("no-at") != "" {
		t.Error("domainOf should be empty without @")
	}
	if truncate("abcdef", 3) != "abc" {
		t.Error("truncate failed")
	}

	h := hasher{key: []byte("k")}
	if h.hash("") != "" {
		t.Error("empty addr should hash to empty")
	}
	if h.hash("A@B.com") != h.hash("a@b.com") {
		t.Error("hash should be case-insensitive")
	}
	if h.hash("a@b.com") == (hasher{key: []byte("other")}).hash("a@b.com") {
		t.Error("different keys should yield different hashes")
	}
}

func TestSpool(t *testing.T) {
	s := NewSpool(filepath.Join(t.TempDir(), "spool.ndjson"))

	if err := s.Append([]byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.Append([]byte(`{"b":2}`)); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Len(); n != 2 {
		t.Fatalf("len = %d, want 2", n)
	}

	// First drain fails everything → all entries retained.
	drained, remaining, err := s.Drain(func([]byte) error { return errors.New("bus down") })
	if err != nil {
		t.Fatal(err)
	}
	if drained != 0 || remaining != 2 {
		t.Fatalf("failed drain: drained=%d remaining=%d", drained, remaining)
	}

	// Second drain succeeds → spool emptied.
	drained, remaining, err = s.Drain(func([]byte) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if drained != 2 || remaining != 0 {
		t.Fatalf("ok drain: drained=%d remaining=%d", drained, remaining)
	}
	if n, _ := s.Len(); n != 0 {
		t.Errorf("len after drain = %d, want 0", n)
	}
}
