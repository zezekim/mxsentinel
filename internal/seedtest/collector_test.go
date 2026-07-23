package seedtest

import (
	"context"
	"errors"
	"testing"
)

// fakeFetcher records the search request and returns a canned result.
type fakeFetcher struct {
	result   FetchResult
	err      error
	gotBoxes []string
	gotTag   string
	gotAddr  string
}

func (f *fakeFetcher) FetchByTag(_ context.Context, acct IMAPAccount, mailboxes []string, tag string) (FetchResult, error) {
	f.gotBoxes = mailboxes
	f.gotTag = tag
	f.gotAddr = acct.Address
	return f.result, f.err
}

func TestIMAPCollectorInbox(t *testing.T) {
	f := &fakeFetcher{result: FetchResult{
		Found:      true,
		Mailbox:    "INBOX",
		RawHeaders: "Authentication-Results: mx.google.com; spf=pass; dkim=pass; dmarc=pass\r\nSubject: probe",
	}}
	c := NewIMAPCollector(IMAPAccount{Address: "seed@gmail.com"}, "gmail", f)

	got, err := c.Collect(context.Background(), "probetag")
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if !got.Found || got.Placement != PlacementInbox {
		t.Errorf("placement = %q found=%v, want inbox/true", got.Placement, got.Found)
	}
	if got.Auth.SPF == nil || !*got.Auth.SPF || got.Auth.DKIM == nil || !*got.Auth.DKIM || got.Auth.DMARC == nil || !*got.Auth.DMARC {
		t.Errorf("auth not all pass: %+v", got.Auth)
	}
	if f.gotTag != "probetag" {
		t.Errorf("searched tag %q", f.gotTag)
	}
	// mailbox list: INBOX first, then gmail spam folders
	if len(f.gotBoxes) < 2 || f.gotBoxes[0] != "INBOX" {
		t.Errorf("mailbox order = %v", f.gotBoxes)
	}
}

func TestIMAPCollectorSpam(t *testing.T) {
	f := &fakeFetcher{result: FetchResult{
		Found:      true,
		Mailbox:    "[Gmail]/Spam",
		RawHeaders: "Authentication-Results: mx.google.com; spf=fail; dkim=fail",
	}}
	c := NewIMAPCollector(IMAPAccount{Address: "seed@gmail.com"}, "gmail", f)
	got, err := c.Collect(context.Background(), "t")
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got.Placement != PlacementSpam {
		t.Errorf("placement = %q, want spam", got.Placement)
	}
	if got.Auth.SPF == nil || *got.Auth.SPF {
		t.Errorf("expected spf=false, got %v", ptrStr(got.Auth.SPF))
	}
}

func TestIMAPCollectorNotFound(t *testing.T) {
	f := &fakeFetcher{result: FetchResult{Found: false}}
	c := NewIMAPCollector(IMAPAccount{Address: "seed@yahoo.com"}, "yahoo", f)
	got, err := c.Collect(context.Background(), "t")
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got.Found || got.Placement != PlacementUnknown {
		t.Errorf("expected not found/unknown, got %+v", got)
	}
}

func TestIMAPCollectorError(t *testing.T) {
	f := &fakeFetcher{err: errors.New("connection refused")}
	c := NewIMAPCollector(IMAPAccount{Address: "seed@gmail.com"}, "gmail", f)
	if _, err := c.Collect(context.Background(), "t"); err == nil {
		t.Error("expected error propagated")
	}
}

func TestIMAPCollectorCustomSpamMailbox(t *testing.T) {
	f := &fakeFetcher{result: FetchResult{Found: false}}
	c := NewIMAPCollector(IMAPAccount{Address: "s@corp.test", SpamMailbox: "Quarantine"}, "other", f)
	if _, err := c.Collect(context.Background(), "t"); err != nil {
		t.Fatal(err)
	}
	if len(f.gotBoxes) != 2 || f.gotBoxes[1] != "Quarantine" {
		t.Errorf("expected [INBOX Quarantine], got %v", f.gotBoxes)
	}
}

func TestExtractAuthHeaders(t *testing.T) {
	raw := "Received: from x\r\n" +
		"Authentication-Results: mx.google.com;\r\n spf=pass;\r\n dkim=pass\r\n" +
		"Authentication-Results: mx2; dmarc=pass\r\n" +
		"Subject: hi\r\n"
	got := extractAuthHeaders(raw)
	if len(got) != 2 {
		t.Fatalf("expected 2 auth headers, got %d: %v", len(got), got)
	}
	// folded continuation should be joined
	auth := ParseAuthResults(got...)
	if auth.SPF == nil || !*auth.SPF || auth.DKIM == nil || !*auth.DKIM || auth.DMARC == nil || !*auth.DMARC {
		t.Errorf("auth parse from folded headers = %+v", auth)
	}
}
