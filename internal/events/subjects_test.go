package events

import (
	"testing"

	"github.com/zezekim/mxsentinel/pkg/contracts"
)

func TestSubject(t *testing.T) {
	cases := []struct {
		t      contracts.EventType
		tenant string
		want   string
	}{
		{contracts.EventSMTPDelivered, "t1", "mxs.smtp.t1.delivered"},
		{contracts.EventDNSValidationFailed, "abc", "mxs.dns.abc.validation_failed"},
		{contracts.EventReputationBlacklistHit, "x", "mxs.reputation.x.blacklist_hit"},
		{contracts.EventAIRCA, "y", "mxs.ai.y.rca"},
	}
	for _, c := range cases {
		if got := Subject(c.t, c.tenant); got != c.want {
			t.Errorf("Subject(%q,%q) = %q, want %q", c.t, c.tenant, got, c.want)
		}
	}
}

func TestDLQSubject(t *testing.T) {
	if got := DLQSubject("smtp"); got != "mxs.dlq.smtp" {
		t.Errorf("DLQSubject = %q", got)
	}
}

func TestStreamForFamily(t *testing.T) {
	for fam, want := range map[string]string{
		"smtp": "SMTP", "dns": "DNS", "reputation": "REPUTATION", "ai": "AI", "nope": "",
	} {
		if got := StreamForFamily(fam); got != want {
			t.Errorf("StreamForFamily(%q) = %q, want %q", fam, got, want)
		}
	}
}
