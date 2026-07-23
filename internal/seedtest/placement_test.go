package seedtest

import (
	"testing"
)

func bp(v bool) *bool { return &v }

func TestNormalizeProvider(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"gmail", ProviderGmail},
		{"GMAIL", ProviderGmail},
		{"mail.google.com", ProviderGmail},
		{"googlemail.com", ProviderGmail},
		{"outlook.com", ProviderOutlook},
		{"hotmail.com", ProviderOutlook},
		{"protection.outlook.com", ProviderOutlook},
		{"live.co.uk", ProviderOutlook},
		{"yahoo.com", ProviderYahoo},
		{"ymail.com", ProviderYahoo},
		{"aol.com", ProviderYahoo},
		{"example.com", ProviderOther},
		{"", ProviderOther},
	}
	for _, c := range cases {
		if got := NormalizeProvider(c.in); got != c.want {
			t.Errorf("NormalizeProvider(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestProviderForAddress(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"seed@gmail.com", ProviderGmail},
		{"seed@outlook.com", ProviderOutlook},
		{"a.b+tag@yahoo.com", ProviderYahoo},
		{"someone@corp.example.com", ProviderOther},
		{"malformed", ProviderOther},
		{"trailing@", ProviderOther},
	}
	for _, c := range cases {
		if got := ProviderForAddress(c.in); got != c.want {
			t.Errorf("ProviderForAddress(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestClassifyMailbox(t *testing.T) {
	cases := []struct {
		in   string
		want Placement
	}{
		{"INBOX", PlacementInbox},
		{"Inbox", PlacementInbox},
		{"[Gmail]/Spam", PlacementSpam},
		{"Junk", PlacementSpam},
		{"Junk Email", PlacementSpam},
		{"Bulk Mail", PlacementSpam},
		{"Archive", PlacementInbox},
		{"INBOX/Promotions", PlacementInbox},
	}
	for _, c := range cases {
		if got := ClassifyMailbox(c.in); got != c.want {
			t.Errorf("ClassifyMailbox(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseAuthResults(t *testing.T) {
	tests := []struct {
		name    string
		headers []string
		spf     *bool
		dkim    *bool
		dmarc   *bool
	}{
		{
			name:    "all pass",
			headers: []string{"mx.google.com; spf=pass smtp.mailfrom=x; dkim=pass header.d=x; dmarc=pass"},
			spf:     bp(true), dkim: bp(true), dmarc: bp(true),
		},
		{
			name:    "spf fail dkim pass no dmarc",
			headers: []string{"mx.example.com; spf=fail; dkim=pass"},
			spf:     bp(false), dkim: bp(true), dmarc: nil,
		},
		{
			name:    "spaces around equals",
			headers: []string{"h; spf = pass ; dmarc = fail"},
			spf:     bp(true), dkim: nil, dmarc: bp(false),
		},
		{
			name:    "last occurrence wins across headers",
			headers: []string{"relay1; dkim=fail", "relay2; dkim=pass"},
			spf:     nil, dkim: bp(true), dmarc: nil,
		},
		{
			name:    "none present",
			headers: []string{"just some text"},
			spf:     nil, dkim: nil, dmarc: nil,
		},
		{
			name:    "case insensitive",
			headers: []string{"H; SPF=Pass; DKIM=PASS"},
			spf:     bp(true), dkim: bp(true), dmarc: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseAuthResults(tt.headers...)
			assertBoolPtr(t, "spf", got.SPF, tt.spf)
			assertBoolPtr(t, "dkim", got.DKIM, tt.dkim)
			assertBoolPtr(t, "dmarc", got.DMARC, tt.dmarc)
		})
	}
}

func assertBoolPtr(t *testing.T, name string, got, want *bool) {
	t.Helper()
	switch {
	case got == nil && want == nil:
		return
	case got == nil || want == nil:
		t.Errorf("%s: got %v, want %v", name, ptrStr(got), ptrStr(want))
	case *got != *want:
		t.Errorf("%s: got %v, want %v", name, *got, *want)
	}
}

func ptrStr(b *bool) string {
	if b == nil {
		return "nil"
	}
	if *b {
		return "true"
	}
	return "false"
}

func TestSummarize(t *testing.T) {
	results := []Result{
		{Provider: "gmail", Placement: PlacementInbox, SPFPass: bp(true), DKIMPass: bp(true), DMARCPass: bp(true)},
		{Provider: "gmail", Placement: PlacementSpam, SPFPass: bp(true), DKIMPass: bp(false), DMARCPass: bp(false)},
		{Provider: "gmail", Placement: PlacementInbox, SPFPass: bp(true)},
		{Provider: "gmail", Placement: PlacementMissing},
		{Provider: "outlook", Placement: PlacementInbox, DKIMPass: bp(true)},
		{Provider: "outlook", Status: StatusSent, Placement: PlacementUnknown}, // pending
		{Provider: "yahoo", Placement: PlacementSpam},
	}
	sum := Summarize(results)

	if sum.Overall.Total != 7 {
		t.Fatalf("overall total = %d, want 7", sum.Overall.Total)
	}
	if sum.Overall.Inbox != 3 || sum.Overall.Spam != 2 || sum.Overall.Missing != 1 || sum.Overall.Pending != 1 {
		t.Errorf("overall counts = inbox %d spam %d missing %d pending %d; want 3/2/1/1",
			sum.Overall.Inbox, sum.Overall.Spam, sum.Overall.Missing, sum.Overall.Pending)
	}

	// providers ordered by total desc: gmail(4), outlook(2), yahoo(1)
	if len(sum.Providers) != 3 {
		t.Fatalf("providers = %d, want 3", len(sum.Providers))
	}
	if sum.Providers[0].Provider != "gmail" || sum.Providers[1].Provider != "outlook" || sum.Providers[2].Provider != "yahoo" {
		t.Errorf("provider order = %v", []string{sum.Providers[0].Provider, sum.Providers[1].Provider, sum.Providers[2].Provider})
	}

	gmail := sum.Providers[0]
	// gmail resolved = inbox2 + spam1 + missing1 = 4
	if gmail.Total != 4 || gmail.Inbox != 2 || gmail.Spam != 1 || gmail.Missing != 1 {
		t.Errorf("gmail counts = %+v", gmail)
	}
	if !approx(gmail.InboxRate, 0.5) {
		t.Errorf("gmail inbox rate = %v, want 0.5", gmail.InboxRate)
	}
	// gmail spf known=3 pass=3 => 1.0; dkim known=2 pass=1 => 0.5; dmarc known=2 pass=1 => 0.5
	if !approx(gmail.SPFPassRate, 1.0) {
		t.Errorf("gmail spf pass rate = %v, want 1.0", gmail.SPFPassRate)
	}
	if !approx(gmail.DKIMPassRate, 0.5) {
		t.Errorf("gmail dkim pass rate = %v, want 0.5", gmail.DKIMPassRate)
	}

	// outlook: 1 inbox resolved, 1 pending -> inbox rate over resolved(1) = 1.0
	outlook := sum.Providers[1]
	if outlook.Pending != 1 || outlook.Inbox != 1 {
		t.Errorf("outlook counts = %+v", outlook)
	}
	if !approx(outlook.InboxRate, 1.0) {
		t.Errorf("outlook inbox rate = %v, want 1.0", outlook.InboxRate)
	}
}

func TestSummarizeEmpty(t *testing.T) {
	sum := Summarize(nil)
	if sum.Overall.Total != 0 || len(sum.Providers) != 0 {
		t.Errorf("empty summarize not zero: %+v", sum)
	}
}

func approx(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-9
}
