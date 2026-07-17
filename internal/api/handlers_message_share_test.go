package api

import (
	"testing"
	"time"

	chstore "github.com/zezekim/mxsentinel/internal/store/clickhouse"
)

func TestShareTokenRoundTrip(t *testing.T) {
	token, prefix, hash, err := GenerateShareToken()
	if err != nil {
		t.Fatalf("GenerateShareToken: %v", err)
	}
	if got := ShareTokenPrefixOf(token); got != prefix {
		t.Errorf("ShareTokenPrefixOf(%q) = %q, want %q", token, got, prefix)
	}
	if !tokenMatches(token, hash) {
		t.Errorf("tokenMatches should accept the real share token")
	}
	if tokenMatches(token+"x", hash) {
		t.Errorf("tokenMatches should reject a tampered share token")
	}
	// A share token must not validate as an API token prefix and vice-versa (distinct schemes).
	if ShareTokenPrefixOf("mxs_deadbeef_cafe") != "" {
		t.Errorf("ShareTokenPrefixOf should reject an api-token (mxs_) prefix")
	}
	if PrefixOf(token) == prefix {
		t.Errorf("api PrefixOf should not treat a share token (mxt_) as its own scheme")
	}
	if ShareTokenPrefixOf("not-a-token") != "" {
		t.Errorf("ShareTokenPrefixOf should be empty for a malformed token")
	}
}

func TestBuildPublicTrace(t *testing.T) {
	base := time.Date(2026, 7, 17, 17, 13, 0, 0, time.UTC)
	trace := chstore.MessageTrace{
		QueueID:    "759CB8A8FD",
		MessageID:  "<abc@calcamino.com>",
		FromDomain: "calcamino.com",
		Events: []chstore.TraceEvent{
			{EventTime: base, EventType: "received", Provider: "", RecipientDomain: "icloud.com"},
			{
				EventTime: base.Add(1 * time.Second), EventType: "rejected", Provider: "apple",
				MXHost: "mx01.mail.icloud.com", RecipientDomain: "icloud.com",
				SMTPCode: 554, EnhancedStatus: "5.7.1", BounceClass: "policy",
				ResponseText: "554 5.7.1 [HM08] Message rejected due to local policy.",
			},
		},
	}

	got := buildPublicTrace("client copy", trace)

	if got.Status != "rejected" {
		t.Errorf("Status = %q, want the latest event type %q", got.Status, "rejected")
	}
	if got.Provider != "apple" {
		t.Errorf("Provider = %q, want %q (last non-empty)", got.Provider, "apple")
	}
	if got.RecipientDomain != "icloud.com" {
		t.Errorf("RecipientDomain = %q, want %q", got.RecipientDomain, "icloud.com")
	}
	if got.FromDomain != "calcamino.com" || got.MessageID != "<abc@calcamino.com>" {
		t.Errorf("summary carry-through wrong: from=%q msgid=%q", got.FromDomain, got.MessageID)
	}
	if got.Label != "client copy" {
		t.Errorf("Label = %q, want %q", got.Label, "client copy")
	}
	if len(got.Events) != 2 {
		t.Fatalf("Events len = %d, want 2", len(got.Events))
	}
	// Events must stay chronological (oldest first) for the timeline UI.
	if got.Events[0].EventType != "received" || got.Events[1].EventType != "rejected" {
		t.Errorf("event order wrong: %q then %q", got.Events[0].EventType, got.Events[1].EventType)
	}
	if got.Events[1].ResponseText == "" || got.Events[1].SMTPCode != 554 {
		t.Errorf("failure event lost its provider response")
	}
}
