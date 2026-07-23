package nlquery

import (
	"context"
	"strings"
	"testing"
	"time"

	chstore "github.com/zezekim/mxsentinel/internal/store/clickhouse"
)

func tContext() context.Context { return context.Background() }

// fakeExecutor returns canned aggregate rows — no ClickHouse needed.
type fakeExecutor struct{}

func (fakeExecutor) DeliverabilityByProvider(_ context.Context, _ string, _, _ time.Time) ([]chstore.ProviderStats, error) {
	return []chstore.ProviderStats{
		{Provider: "yahoo", Delivered: 60, Deferred: 5, Bounced: 5, Rejected: 30, Total: 100},
		{Provider: "google", Delivered: 95, Deferred: 2, Bounced: 1, Rejected: 2, Total: 100},
	}, nil
}

func (fakeExecutor) RejectionGroups(_ context.Context, _ string, _, _ time.Time, _ int) ([]chstore.RejectionGroup, error) {
	return []chstore.RejectionGroup{
		{SMTPCode: 550, EnhancedStatus: "5.7.1", Provider: "yahoo", Sample: "blocked due to reputation", Count: 25},
	}, nil
}

func (fakeExecutor) TopSenders(_ context.Context, _, _, _ string, _ time.Time, _ int) ([]chstore.SenderCount, error) {
	return []chstore.SenderCount{{Key: "1.2.3.4", Count: 500}}, nil
}

func (fakeExecutor) DMARCAlignmentSummary(_ context.Context, _, _ string, _, _ time.Time) (chstore.DMARCAlignment, error) {
	return chstore.DMARCAlignment{Total: 1000, DKIMAligned: 950, SPFAligned: 900}, nil
}

// mockClient plays the two-call flow: first call returns a plan, second returns an answer.
type mockClient struct {
	planJSON string
	answer   string
	calls    int
	gotUser  []string
}

func (m *mockClient) Complete(_ context.Context, _ string, user string) (string, error) {
	m.calls++
	m.gotUser = append(m.gotUser, user)
	if m.calls == 1 {
		return m.planJSON, nil
	}
	return m.answer, nil
}

func TestAnswerHappyPath(t *testing.T) {
	mc := &mockClient{
		planJSON: `{"queries":[{"tool":"deliverability_by_provider","args":{"window":"7d"}}]}`,
		answer:   "Yahoo delivered 60% vs Google 95%.",
	}
	res, err := Answer(tContext(), mc, fakeExecutor{}, Config{MaxTools: 3}, "tenant-1", "why did mail to yahoo drop?")
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if mc.calls != 2 {
		t.Errorf("expected 2 llm calls, got %d", mc.calls)
	}
	if res.Answer != "Yahoo delivered 60% vs Google 95%." {
		t.Errorf("answer = %q", res.Answer)
	}
	if len(res.UsedQueries) != 1 || res.UsedQueries[0] != "deliverability_by_provider" {
		t.Errorf("used = %v", res.UsedQueries)
	}
	if len(res.Data) != 1 || len(res.Data[0].Rows) != 2 {
		t.Errorf("unexpected data: %+v", res.Data)
	}
	// The answer-composition prompt must carry the aggregate data (call 2's user message).
	if len(mc.gotUser) < 2 || !strings.Contains(mc.gotUser[1], "yahoo") {
		t.Errorf("answer prompt missing aggregate data")
	}
}

func TestAnswerRejectsOffWhitelistTool(t *testing.T) {
	mc := &mockClient{planJSON: `{"queries":[{"tool":"raw_sql","args":{}}]}`}
	_, err := Answer(tContext(), mc, fakeExecutor{}, Config{MaxTools: 3}, "t", "q")
	if err == nil || !strings.Contains(err.Error(), "whitelist") {
		t.Fatalf("expected whitelist rejection, got %v", err)
	}
	if mc.calls != 1 {
		t.Errorf("should not reach answer step; calls=%d", mc.calls)
	}
}

func TestAnswerRejectsForbiddenArg(t *testing.T) {
	mc := &mockClient{planJSON: `{"queries":[{"tool":"deliverability_by_provider","args":{"subject":"hi"}}]}`}
	_, err := Answer(tContext(), mc, fakeExecutor{}, Config{MaxTools: 3}, "t", "read my subjects")
	if err == nil {
		t.Fatal("expected forbidden-arg rejection")
	}
}

func TestValidatePlanCaps(t *testing.T) {
	p := plan{Queries: []PlannedQuery{
		{Tool: "deliverability_by_provider"},
		{Tool: "rejection_reasons"},
		{Tool: "dmarc_alignment"},
	}}
	out, err := ValidatePlan(p, 2)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(out) != 2 {
		t.Errorf("expected cap at 2, got %d", len(out))
	}
}

func TestBuildPlanPromptListsToolsNoContent(t *testing.T) {
	sys, user := BuildPlanPrompt("how is gmail doing?")
	if !strings.Contains(sys, "deliverability_by_provider") {
		t.Error("plan prompt missing tool catalog")
	}
	if !strings.Contains(sys, "never available") && !strings.Contains(sys, "CANNOT read message bodies") {
		t.Error("plan prompt missing privacy instruction")
	}
	for _, bad := range []string{"message body", "subject line"} {
		if strings.Contains(strings.ToLower(user), bad) {
			t.Errorf("user prompt unexpectedly contains %q", bad)
		}
	}
}

func TestParsePlanFenced(t *testing.T) {
	raw := "Sure!\n```json\n{\"queries\":[{\"tool\":\"top_senders\",\"args\":{\"dimension\":\"ip\",\"metric\":\"volume\"}}]}\n```"
	p, err := parsePlan(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(p.Queries) != 1 || p.Queries[0].Tool != "top_senders" {
		t.Errorf("unexpected plan: %+v", p)
	}
}
