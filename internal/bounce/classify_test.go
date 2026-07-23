package bounce

import (
	"strings"
	"testing"
	"time"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		name     string
		code     int
		enhanced string
		text     string
		want     Category
	}{
		// invalid recipient — enhanced code driven
		{"dsn 5.1.1 bad mailbox", 550, "5.1.1", "", CategoryInvalidRecipient},
		{"dsn 5.1.2 bad system", 550, "5.1.2", "host not found", CategoryInvalidRecipient},
		{"dsn 5.1.6 mailbox moved", 550, "5.1.6", "", CategoryInvalidRecipient},
		{"dsn 5.1.10 null mx", 550, "5.1.10", "", CategoryInvalidRecipient},
		// invalid recipient — text driven
		{"user unknown text", 550, "", "550 5.5.0 user unknown", CategoryInvalidRecipient},
		{"no such user", 550, "", "No such user here", CategoryInvalidRecipient},
		{"recipient address rejected", 550, "", "Recipient address rejected: User unknown in virtual mailbox table", CategoryInvalidRecipient},
		{"does not exist", 550, "", "The email account that you tried to reach does not exist", CategoryInvalidRecipient},

		// mailbox full
		{"dsn 4.2.2 full transient", 452, "4.2.2", "", CategoryMailboxFull},
		{"dsn 5.2.2 full", 552, "5.2.2", "", CategoryMailboxFull},
		{"over quota text", 452, "", "Mailbox full / over quota", CategoryMailboxFull},
		{"insufficient system storage", 452, "4.2.2", "Requested mail action aborted: insufficient system storage", CategoryMailboxFull},

		// reputation (checked before spam)
		{"spamhaus listed is reputation", 554, "5.7.1", "Service unavailable; Client host blocked using Spamhaus", CategoryReputation},
		{"blocklist", 554, "", "550 rejected: listed on a blocklist", CategoryReputation},
		{"bad reputation", 421, "4.7.0", "your ip has a poor reputation", CategoryReputation},
		{"barracuda", 550, "", "rejected by Barracuda Reputation Block List", CategoryReputation},

		// spam / content
		{"marked as spam", 550, "5.7.1", "message identified as spam", CategorySpamBlock},
		{"bulk", 550, "", "552 message looks like bulk mail", CategorySpamBlock},
		{"content rejected", 550, "5.6.0", "content rejected", CategorySpamBlock},
		{"phishing", 550, "", "rejected due to phishing content", CategorySpamBlock},

		// greylisting is transient (must beat reputation/block)
		{"greylist explicit", 451, "4.7.1", "Greylisted, please try again later", CategorySoft},
		{"graylist spelling", 451, "4.2.0", "graylisted, come back later", CategorySoft},

		// policy / block
		{"dsn 5.7.1 generic policy", 550, "5.7.1", "delivery not authorized", CategoryBlock},
		{"access denied", 550, "", "550 access denied", CategoryBlock},
		{"administratively prohibited", 550, "", "administratively prohibited", CategoryBlock},

		// permanence fallback
		{"hard by code no text", 550, "", "550 requested action not taken", CategoryHard},
		{"hard by enhanced class", 550, "5.3.0", "other or undefined mail system status", CategoryHard},
		{"soft by 4xx code", 451, "", "451 temporary local problem", CategorySoft},
		{"soft by enhanced class", 450, "4.4.1", "no answer from host", CategorySoft},

		// unknown residual
		{"no signal at all", 0, "", "", CategoryUnknown},
		{"2xx is not a failure", 250, "2.0.0", "ok queued", CategoryUnknown},
		{"garbled enhanced", 0, "not.a.code.x", "", CategoryUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Classify(tt.code, tt.enhanced, tt.text)
			if got != tt.want {
				t.Fatalf("Classify(%d, %q, %q) = %q, want %q", tt.code, tt.enhanced, tt.text, got, tt.want)
			}
		})
	}
}

func TestClassifyCaseInsensitive(t *testing.T) {
	upper := Classify(550, "", "USER UNKNOWN")
	lower := Classify(550, "", "user unknown")
	if upper != lower || upper != CategoryInvalidRecipient {
		t.Fatalf("case sensitivity leak: upper=%q lower=%q", upper, lower)
	}
}

func TestCategoryTransient(t *testing.T) {
	transient := map[Category]bool{CategorySoft: true, CategoryMailboxFull: true}
	all := []Category{
		CategoryHard, CategorySoft, CategoryBlock, CategorySpamBlock,
		CategoryInvalidRecipient, CategoryMailboxFull, CategoryReputation, CategoryUnknown,
	}
	for _, c := range all {
		if got := c.Transient(); got != transient[c] {
			t.Errorf("%q.Transient() = %v, want %v", c, got, transient[c])
		}
	}
}

func TestSuppressionFor(t *testing.T) {
	tests := []struct {
		cat       Category
		suppress  bool
		reason    string
		permanent bool // TTL == 0
	}{
		{CategoryInvalidRecipient, true, "invalid_recipient", true},
		{CategoryHard, true, "hard_bounce", true},
		{CategorySpamBlock, true, "spam_block", false},
		{CategorySoft, false, "", false},
		{CategoryMailboxFull, false, "", false},
		{CategoryReputation, false, "", false},
		{CategoryBlock, false, "", false},
		{CategoryUnknown, false, "", false},
	}
	for _, tt := range tests {
		d := SuppressionFor(tt.cat)
		if d.Suppress != tt.suppress {
			t.Errorf("SuppressionFor(%q).Suppress = %v, want %v", tt.cat, d.Suppress, tt.suppress)
		}
		if d.Reason != tt.reason {
			t.Errorf("SuppressionFor(%q).Reason = %q, want %q", tt.cat, d.Reason, tt.reason)
		}
		// Permanence is only meaningful for categories that actually suppress.
		if tt.suppress && (d.TTL == 0) != tt.permanent {
			t.Errorf("SuppressionFor(%q).TTL permanence = %v, want permanent=%v", tt.cat, d.TTL == 0, tt.permanent)
		}
	}
}

func TestSuppressionPriorityOrdering(t *testing.T) {
	// Invalid recipient must outrank a plain hard bounce, which must outrank spam.
	inv := SuppressionFor(CategoryInvalidRecipient).Priority
	hard := SuppressionFor(CategoryHard).Priority
	spam := SuppressionFor(CategorySpamBlock).Priority
	if !(inv > hard && hard > spam) {
		t.Fatalf("priority ordering wrong: invalid=%d hard=%d spam=%d", inv, hard, spam)
	}
}

func TestExpiryFor(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if exp := SuppressionFor(CategoryHard).ExpiryFor(now); exp != nil {
		t.Errorf("permanent suppression should have nil expiry, got %v", exp)
	}
	exp := SuppressionFor(CategorySpamBlock).ExpiryFor(now)
	if exp == nil {
		t.Fatalf("spam_block suppression should have an expiry")
	}
	if want := now.Add(spamSuppressTTL); !exp.Equal(want) {
		t.Errorf("spam_block expiry = %v, want %v", exp, want)
	}
}

func TestBuildExportPlain(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	recs := []SuppressionRecord{
		{RecipientHash: "ccc", Reason: "hard_bounce"},
		{RecipientHash: "aaa", Reason: "invalid_recipient"},
		{RecipientHash: "", Reason: "blank should be skipped"},
		{RecipientHash: "bbb", Reason: "spam_block"},
	}
	out := BuildExport(ExportFormatPlain, recs, now)
	lines := nonComment(out)
	want := []string{"aaa", "bbb", "ccc"} // sorted, blank dropped
	if len(lines) != len(want) {
		t.Fatalf("plain export lines = %v, want %v", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, lines[i], want[i])
		}
	}
	if !strings.Contains(out, "generated_at=2026-01-01T12:00:00Z") {
		t.Errorf("missing/incorrect header provenance:\n%s", out)
	}
}

func TestBuildExportPostfix(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	recs := []SuppressionRecord{
		{RecipientHash: "deadbeef", Reason: "invalid_recipient", Category: "invalid_recipient"},
	}
	out := BuildExport(ExportFormatPostfix, recs, now)
	lines := nonComment(out)
	if len(lines) != 1 {
		t.Fatalf("expected 1 data line, got %v", lines)
	}
	want := "deadbeef REJECT MX Sentinel suppressed: invalid_recipient"
	if lines[0] != want {
		t.Errorf("postfix line = %q, want %q", lines[0], want)
	}
}

func TestBuildExportNeverLeaksAddress(t *testing.T) {
	// Defensive: the record has no address field, but ensure the reason text (operator
	// controlled) can't inject a newline that would corrupt the line-oriented map.
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	out := BuildExport(ExportFormatPostfix, []SuppressionRecord{
		{RecipientHash: "h1", Reason: "bad\nrecipient@example.com"},
	}, now)
	if strings.Count(out, "\n") != 4 { // 3 header lines + 1 data line, no embedded newline
		t.Errorf("newline in reason not sanitised:\n%s", out)
	}
}

// nonComment returns the non-empty, non-comment lines of an export.
func nonComment(s string) []string {
	var out []string
	for _, l := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		out = append(out, l)
	}
	return out
}
