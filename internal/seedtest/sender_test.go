package seedtest

import (
	"context"
	"net/smtp"
	"strings"
	"testing"
	"time"
)

func TestPlusAddress(t *testing.T) {
	cases := []struct {
		addr, tag, want string
	}{
		{"seed@gmail.com", "abc123", "seed+abc123@gmail.com"},
		{"seed+old@gmail.com", "abc123", "seed+abc123@gmail.com"}, // existing plus segment replaced
		{"nodomain", "abc123", "nodomain+abc123"},
	}
	for _, c := range cases {
		if got := PlusAddress(c.addr, c.tag); got != c.want {
			t.Errorf("PlusAddress(%q,%q) = %q, want %q", c.addr, c.tag, got, c.want)
		}
	}
}

func TestNewProbeTagUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		tag := NewProbeTag()
		if tag == "" {
			t.Fatal("empty tag")
		}
		if seen[tag] {
			t.Fatalf("duplicate tag %q", tag)
		}
		seen[tag] = true
	}
}

func TestBuildProbe(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	probe := BuildProbe("sender@relay.test", "seed@gmail.com", "runtag1", "probetag1", now)

	if probe.From != "sender@relay.test" {
		t.Errorf("From = %q", probe.From)
	}
	if probe.To != "seed@gmail.com" {
		t.Errorf("To (envelope) = %q, want bare seed", probe.To)
	}
	raw := string(probe.Raw)

	mustContain := []string{
		TagHeader + ": probetag1",
		RunTagHeader + ": runtag1",
		"To: seed+probetag1@gmail.com", // plus-address in header
		"From: sender@relay.test",
		"Subject: MX Sentinel inbox placement probe [probetag1]",
		"Message-ID: <probetag1.runtag1@mxsentinel.seedtest>",
		"MIME-Version: 1.0",
	}
	for _, s := range mustContain {
		if !strings.Contains(raw, s) {
			t.Errorf("probe missing %q\n---\n%s", s, raw)
		}
	}
	// header/body separator present
	if !strings.Contains(raw, "\r\n\r\n") {
		t.Error("probe missing header/body separator")
	}
}

func TestBuildProbeHeaderInjectionSafe(t *testing.T) {
	// A malicious "from" carrying CRLF must not inject extra headers.
	probe := BuildProbe("evil@x\r\nBcc: victim@y", "seed@gmail.com", "r", "p", time.Now())
	raw := string(probe.Raw)
	if strings.Contains(raw, "\r\nBcc:") {
		t.Errorf("CRLF injection not neutralized:\n%s", raw)
	}
}

type fakeSend struct {
	addr string
	from string
	to   []string
	msg  []byte
	err  error
	n    int
}

func (f *fakeSend) fn(addr string, _ smtp.Auth, from string, to []string, msg []byte) error {
	f.n++
	f.addr, f.from, f.to, f.msg = addr, from, to, msg
	return f.err
}

func TestSMTPSenderSend(t *testing.T) {
	fs := &fakeSend{}
	sender := NewSMTPSender(SMTPConfig{Host: "relay.test", Port: 587}, fs.fn)

	probe := BuildProbe("from@relay.test", "seed@gmail.com", "r", "p", time.Now())
	if err := sender.Send(context.Background(), probe); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if fs.n != 1 {
		t.Fatalf("send called %d times, want 1", fs.n)
	}
	if fs.addr != "relay.test:587" {
		t.Errorf("addr = %q, want relay.test:587", fs.addr)
	}
	if fs.from != "from@relay.test" {
		t.Errorf("from = %q", fs.from)
	}
	if len(fs.to) != 1 || fs.to[0] != "seed@gmail.com" {
		t.Errorf("to = %v, want [seed@gmail.com]", fs.to)
	}
}

func TestSMTPSenderRejectsEmpty(t *testing.T) {
	fs := &fakeSend{}
	sender := NewSMTPSender(SMTPConfig{Host: "relay.test"}, fs.fn)
	if err := sender.Send(context.Background(), ProbeMessage{From: "a@b", To: ""}); err == nil {
		t.Error("expected error for empty recipient")
	}
	if err := sender.Send(context.Background(), ProbeMessage{From: "", To: "a@b"}); err == nil {
		t.Error("expected error for empty sender")
	}
	if fs.n != 0 {
		t.Errorf("send should not be called, got %d", fs.n)
	}
}

func TestSMTPSenderContextCanceled(t *testing.T) {
	fs := &fakeSend{}
	sender := NewSMTPSender(SMTPConfig{Host: "relay.test"}, fs.fn)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	probe := BuildProbe("from@relay.test", "seed@gmail.com", "r", "p", time.Now())
	if err := sender.Send(ctx, probe); err == nil {
		t.Error("expected context error")
	}
	if fs.n != 0 {
		t.Errorf("send should not run on canceled ctx, got %d", fs.n)
	}
}
