package snds

import "testing"

// A JMRP ARF complaint as Microsoft sends it: multipart/report; report-type=feedback-report,
// with a Source-IP field in the machine-readable part and the original message's headers in a
// message/rfc822 part. Mirrors internal/fbl's ARF fixture but adds the sending-IP attribution
// that JMRP carries and SNDS reputation joins on.
const sampleJMRP = "From: <staff@hotmail.com>\r\n" +
	"Date: Thu, 8 Mar 2026 00:00:00 -0000\r\n" +
	"Subject: complaint about message from 203.0.113.11\r\n" +
	"To: <jmrp@relay.example>\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/report; report-type=feedback-report;\r\n" +
	"\tboundary=\"BOUND\"\r\n" +
	"\r\n" +
	"--BOUND\r\n" +
	"Content-Type: text/plain; charset=\"US-ASCII\"\r\n" +
	"\r\n" +
	"This is an email abuse report from the Microsoft JMRP.\r\n" +
	"\r\n" +
	"--BOUND\r\n" +
	"Content-Type: message/feedback-report\r\n" +
	"\r\n" +
	"Feedback-Type: abuse\r\n" +
	"User-Agent: Microsoft-SNDS-Feedback/1.0\r\n" +
	"Version: 1\r\n" +
	"Original-Mail-From: <bounce@marketing.client.example>\r\n" +
	"Source-IP: 203.0.113.11\r\n" +
	"\r\n" +
	"--BOUND\r\n" +
	"Content-Type: message/rfc822\r\n" +
	"\r\n" +
	"Received: from mail.client.example ([203.0.113.11]) by mx.hotmail.com\r\n" +
	"From: Sales <sales@client.example>\r\n" +
	"To: <victim@hotmail.com>\r\n" +
	"Subject: Big sale!\r\n" +
	"Message-ID: <abc123@mail.client.example>\r\n" +
	"\r\n" +
	"body that we should ignore\r\n" +
	"--BOUND--\r\n"

func TestParseJMRP(t *testing.T) {
	c, err := ParseJMRP([]byte(sampleJMRP))
	if err != nil {
		t.Fatalf("ParseJMRP: %v", err)
	}
	if c.FeedbackType != "abuse" {
		t.Errorf("FeedbackType = %q, want abuse", c.FeedbackType)
	}
	if c.SenderDomain != "client.example" {
		t.Errorf("SenderDomain = %q, want client.example", c.SenderDomain)
	}
	if c.SourceIP != "203.0.113.11" {
		t.Errorf("SourceIP = %q, want 203.0.113.11", c.SourceIP)
	}
	if c.Provider != "microsoft" {
		t.Errorf("Provider = %q, want microsoft", c.Provider)
	}
	if c.MessageID != "abc123@mail.client.example" {
		t.Errorf("MessageID = %q, want abc123@mail.client.example", c.MessageID)
	}
}

// When the feedback-report omits Source-IP, we recover the sending IP from the first bracketed
// literal in the embedded Received: header.
func TestParseJMRP_SourceIPFromReceived(t *testing.T) {
	const raw = "From: <staff@hotmail.com>\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/report; report-type=feedback-report; boundary=\"B\"\r\n" +
		"\r\n" +
		"--B\r\n" +
		"Content-Type: message/feedback-report\r\n" +
		"\r\n" +
		"Feedback-Type: abuse\r\n" +
		"Original-Mail-From: notify@shop.client.example\r\n" +
		"\r\n" +
		"--B\r\n" +
		"Content-Type: message/rfc822\r\n" +
		"\r\n" +
		"Received: from relay.example ([198.51.100.5]) by mx.hotmail.com\r\n" +
		"From: <notify@shop.client.example>\r\n" +
		"\r\n" +
		"--B--\r\n"
	c, err := ParseJMRP([]byte(raw))
	if err != nil {
		t.Fatalf("ParseJMRP: %v", err)
	}
	if c.SourceIP != "198.51.100.5" {
		t.Errorf("SourceIP = %q, want 198.51.100.5", c.SourceIP)
	}
	if c.SenderDomain != "shop.client.example" {
		t.Errorf("SenderDomain = %q, want shop.client.example", c.SenderDomain)
	}
}

func TestParseJMRP_NotMultipart(t *testing.T) {
	const raw = "From: a@b\r\nContent-Type: text/plain\r\n\r\nhello\r\n"
	if _, err := ParseJMRP([]byte(raw)); err == nil {
		t.Fatal("expected error for non-multipart message")
	}
}

func TestFirstIP(t *testing.T) {
	cases := map[string]string{
		"from x ([203.0.113.11]) by y": "203.0.113.11",
		" 198.51.100.5 ":               "198.51.100.5",
		"[2001:db8::1]":                "2001:db8::1",
		"0.0.0.0":                      "", // unspecified rejected
		"no ip here":                   "",
	}
	for in, want := range cases {
		if got := firstIP(in); got != want {
			t.Errorf("firstIP(%q) = %q, want %q", in, got, want)
		}
	}
}
