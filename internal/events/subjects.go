package events

import (
	"fmt"

	"github.com/zezekim/mxsentinel/pkg/contracts"
)

// SubjectPrefix is the root token for all MX Sentinel bus subjects.
const SubjectPrefix = "mxs"

// streamDefs maps JetStream stream names to the subject patterns they capture.
// See docs/event-contracts.md.
var streamDefs = map[string][]string{
	"SMTP":       {"mxs.smtp.>"},
	"DNS":        {"mxs.dns.>"},
	"REPUTATION": {"mxs.reputation.>"},
	"AI":         {"mxs.ai.>"},
	"DLQ":        {"mxs.dlq.>"},
}

// Subject builds the publish subject for an event:
//
//	mxs.<family>.<tenant_id>.<event>   e.g. mxs.smtp.<tenant>.delivered
func Subject(t contracts.EventType, tenantID string) string {
	fam := t.Family()
	event := string(t)[len(fam)+1:] // part after "family."
	return fmt.Sprintf("%s.%s.%s.%s", SubjectPrefix, fam, tenantID, event)
}

// DLQSubject is where poison/invalid messages of a family are routed.
func DLQSubject(family string) string {
	return fmt.Sprintf("%s.dlq.%s", SubjectPrefix, family)
}

// StreamForFamily returns the JetStream stream name carrying a family's events.
func StreamForFamily(family string) string {
	switch family {
	case "smtp":
		return "SMTP"
	case "dns":
		return "DNS"
	case "reputation":
		return "REPUTATION"
	case "ai":
		return "AI"
	default:
		return ""
	}
}
