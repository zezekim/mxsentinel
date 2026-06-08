package fbl

import "testing"

// A canonical RFC 5965 ARF complaint: multipart/report with a human-readable part, a
// message/feedback-report part, and the original message's headers as message/rfc822.
const sampleARF = "From: <feedback@yahoo.example>\r\n" +
	"Date: Thu, 8 Mar 2026 00:00:00 -0000\r\n" +
	"Subject: complaint about message from 192.0.2.1\r\n" +
	"To: <fbl@relay.example>\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/report; report-type=feedback-report;\r\n" +
	"\tboundary=\"BOUND\"\r\n" +
	"\r\n" +
	"--BOUND\r\n" +
	"Content-Type: text/plain; charset=\"US-ASCII\"\r\n" +
	"\r\n" +
	"This is an email abuse report for an email message received on 2026-03-08.\r\n" +
	"\r\n" +
	"--BOUND\r\n" +
	"Content-Type: message/feedback-report\r\n" +
	"\r\n" +
	"Feedback-Type: abuse\r\n" +
	"User-Agent: Yahoo!-Mail-Feedback/2.0\r\n" +
	"Version: 1\r\n" +
	"Original-Mail-From: <bounce@marketing.client.example>\r\n" +
	"Reported-Domain: client.example\r\n" +
	"\r\n" +
	"--BOUND\r\n" +
	"Content-Type: message/rfc822\r\n" +
	"\r\n" +
	"From: Sales <sales@client.example>\r\n" +
	"To: <victim@aol.example>\r\n" +
	"Subject: Big sale!\r\n" +
	"Message-ID: <abc123@mail.client.example>\r\n" +
	"X-Mxs-Sasl-Username: mailer@relay.example\r\n" +
	"\r\n" +
	"body that we should ignore\r\n" +
	"--BOUND--\r\n"

func TestParseARF(t *testing.T) {
	pc, err := ParseARF([]byte(sampleARF))
	if err != nil {
		t.Fatalf("ParseARF: %v", err)
	}
	if pc.FeedbackType != "abuse" {
		t.Errorf("FeedbackType = %q, want abuse", pc.FeedbackType)
	}
	// From: of the original message wins over Original-Mail-From / Reported-Domain.
	if pc.SenderDomain != "client.example" {
		t.Errorf("SenderDomain = %q, want client.example", pc.SenderDomain)
	}
	if pc.MessageID != "abc123@mail.client.example" {
		t.Errorf("MessageID = %q, want abc123@mail.client.example", pc.MessageID)
	}
	if pc.SASLUsername != "mailer@relay.example" {
		t.Errorf("SASLUsername = %q, want mailer@relay.example", pc.SASLUsername)
	}
	if pc.Provider != "yahoo" {
		t.Errorf("Provider = %q, want yahoo", pc.Provider)
	}
}

// When the report has no embedded original message (headers-only providers sometimes omit
// it), we still recover the sender domain from the feedback-report's Original-Mail-From.
func TestParseARF_FeedbackReportFallback(t *testing.T) {
	const raw = "From: <fbl@google.example>\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/report; report-type=feedback-report; boundary=\"B\"\r\n" +
		"\r\n" +
		"--B\r\n" +
		"Content-Type: message/feedback-report\r\n" +
		"\r\n" +
		"Feedback-Type: fraud\r\n" +
		"Original-Mail-From: notify@shop.client.example\r\n" +
		"\r\n" +
		"--B--\r\n"
	pc, err := ParseARF([]byte(raw))
	if err != nil {
		t.Fatalf("ParseARF: %v", err)
	}
	if pc.FeedbackType != "fraud" {
		t.Errorf("FeedbackType = %q, want fraud", pc.FeedbackType)
	}
	if pc.SenderDomain != "shop.client.example" {
		t.Errorf("SenderDomain = %q, want shop.client.example", pc.SenderDomain)
	}
	if pc.Provider != "google" {
		t.Errorf("Provider = %q, want google", pc.Provider)
	}
}

func TestParseARF_NotMultipart(t *testing.T) {
	const raw = "From: a@b\r\nContent-Type: text/plain\r\n\r\nhello\r\n"
	if _, err := ParseARF([]byte(raw)); err == nil {
		t.Fatal("expected error for non-multipart message")
	}
}

func TestNormalizeReputation(t *testing.T) {
	cases := map[string]string{
		"HIGH": "HIGH", "low": "LOW", "Bad": "BAD",
		"REPUTATION_CATEGORY_UNSPECIFIED": "", "": "",
	}
	for in, want := range cases {
		if got := normalizeReputation(in); got != want {
			t.Errorf("normalizeReputation(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsBadReputation(t *testing.T) {
	for _, r := range []string{"LOW", "BAD", "low", "bad"} {
		if !IsBadReputation(r) {
			t.Errorf("IsBadReputation(%q) = false, want true", r)
		}
	}
	for _, r := range []string{"HIGH", "GOOD", "MEDIUM", ""} {
		if IsBadReputation(r) {
			t.Errorf("IsBadReputation(%q) = true, want false", r)
		}
	}
}
