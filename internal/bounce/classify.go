// Package bounce is the platform's Remediate step: it classifies SMTP bounces/DSNs that
// already flow through telemetry into actionable categories and maintains a per-tenant
// suppression list derived from them.
//
// The classifier (Classify) is a PURE, table-driven function of
// (smtp_code, enhanced_code, response_text) -> Category. It performs no I/O and holds no
// state, so it is exhaustively unit-tested (classify_test.go) and can be reused by the
// bounced daemon, the API feed, and any relay-side hook without divergence.
//
// PRIVACY BOUNDARY (docs/data-model.md): this package never sees message bodies or subject
// lines. Recipient addresses are handled only as the keyed hash produced at the telemetry
// parser boundary (internal/telemetry). response_text is the remote-MTA diagnostic line,
// already truncated upstream.
package bounce

import "strings"

// Category is the classified reason a message bounced or was rejected. It mixes reason
// (invalid_recipient, mailbox_full, spam_block, reputation, block) with permanence
// (hard, soft) for the residual cases that carry no more specific signal.
type Category string

const (
	// CategoryHard is a permanent failure with no more specific cause (5xx / DSN 5.x.x).
	CategoryHard Category = "hard"
	// CategorySoft is a transient failure that may succeed on retry (4xx / DSN 4.x.x,
	// greylisting, temporary resource problems).
	CategorySoft Category = "soft"
	// CategoryBlock is an administrative/policy block that is not specifically spam or
	// reputation (DSN x.7.1, "access denied", "not allowed by policy").
	CategoryBlock Category = "block"
	// CategorySpamBlock is a rejection because the message was classified as spam/bulk by
	// the receiver's content filter.
	CategorySpamBlock Category = "spam_block"
	// CategoryInvalidRecipient is a non-existent / moved / unroutable mailbox
	// (DSN x.1.1/x.1.2/x.1.6, "user unknown", "no such user").
	CategoryInvalidRecipient Category = "invalid_recipient"
	// CategoryMailboxFull is a recipient over quota (DSN x.2.2, "mailbox full", "over quota").
	CategoryMailboxFull Category = "mailbox_full"
	// CategoryReputation is a block attributed to sending IP/domain reputation or a DNSBL
	// listing ("poor reputation", "listed on", "spamhaus", "blocklist").
	CategoryReputation Category = "reputation"
	// CategoryUnknown is the residual bucket when neither codes nor text carry a usable
	// signal (e.g. a 2xx status or a garbled response).
	CategoryUnknown Category = "unknown"
)

// Transient reports whether a category is expected to be retryable rather than terminal.
// Used by suppression policy and rate rollups.
func (c Category) Transient() bool {
	switch c {
	case CategorySoft, CategoryMailboxFull:
		return true
	default:
		return false
	}
}

// Pattern sets. These are intentionally lower-case substrings matched against the
// lower-cased response text. Order of the checks in Classify matters (first match wins);
// see the per-check comments.
var (
	greylistPatterns = []string{
		"greylist", "grey-list", "graylist", "gray-list",
		"deferred due to policy", "come back later", "try again later",
	}
	invalidRecipientPatterns = []string{
		"user unknown", "unknown user", "no such user", "no such recipient",
		"recipient unknown", "unknown recipient", "does not exist", "doesn't exist",
		"invalid recipient", "invalid mailbox", "recipient rejected",
		"recipient address rejected", "address rejected", "mailbox unavailable",
		"no mailbox", "mailbox not found", "unrouteable address", "unroutable address",
		"account has been disabled", "account is disabled", "user not found",
		"recipient not found", "not our customer", "user doesn't have",
	}
	mailboxFullPatterns = []string{
		"mailbox full", "mailbox is full", "over quota", "over the quota",
		"quota exceeded", "exceeded storage", "insufficient system storage",
		"out of storage", "recipient storage", "user is over",
	}
	// Reputation is checked before spam so that DNSBL/IP-reputation wording (which often
	// contains the substring "spam", e.g. "spamhaus") is not miscategorised as content spam.
	reputationPatterns = []string{
		"reputation", "blacklist", "black list", "blocklist", "block list",
		"denylist", "deny list", "dnsbl", "rbl", "spamhaus", "spamcop",
		"barracuda", "sorbs", "listed on", "listed in", "listed by",
		"listed at", "poor ip", "bad reputation", "sender ip",
	}
	spamPatterns = []string{
		"spam", "bulk", "junk", "unsolicited", "message content",
		"content rejected", "looks like spam", "identified as spam",
		"marked as spam", "high probability of spam", "phishing",
	}
	blockPatterns = []string{
		"access denied", "not allowed", "not permitted", "policy",
		"administrative prohibition", "administratively prohibited",
		"blocked", "refused", "rejected by", "connection refused",
		"delivery not authorized",
	}
)

// Classify maps a delivery failure's (smtp_code, enhanced_code, response_text) to a
// Category. It is pure: no I/O, no state, safe for concurrent use.
//
//   - smtpCode is the 3-digit SMTP reply code (e.g. 550); 0 if unknown.
//   - enhancedCode is the RFC 3463 enhanced status (e.g. "5.1.1"); "" if absent.
//   - responseText is the remote-MTA diagnostic text (already truncated by the parser).
//
// The order of checks is deliberate — the most specific reason wins, and permanence
// (hard vs soft) is only the fallback when no reason is evident.
func Classify(smtpCode int, enhancedCode, responseText string) Category {
	text := strings.ToLower(responseText)
	class, subject, detail := enhancedParts(enhancedCode)

	// 1. Greylisting is transient by design; its wording ("...try again later") and DSN
	//    4.7.1 would otherwise be captured by the reputation/block checks below.
	if containsAny(text, greylistPatterns) {
		return CategorySoft
	}

	// 2. Invalid recipient — the highest-value signal for suppression. DSN x.1.1 (bad
	//    mailbox), x.1.2 (bad system), x.1.6 (mailbox moved), x.1.10 (null MX).
	if subject == "1" && (detail == "1" || detail == "2" || detail == "6" || detail == "10") {
		return CategoryInvalidRecipient
	}
	if containsAny(text, invalidRecipientPatterns) {
		return CategoryInvalidRecipient
	}

	// 3. Mailbox full — DSN x.2.2, or explicit quota wording.
	if subject == "2" && detail == "2" {
		return CategoryMailboxFull
	}
	if containsAny(text, mailboxFullPatterns) {
		return CategoryMailboxFull
	}

	// 4. Reputation before spam (see reputationPatterns comment).
	if containsAny(text, reputationPatterns) {
		return CategoryReputation
	}

	// 5. Content spam / bulk.
	if containsAny(text, spamPatterns) {
		return CategorySpamBlock
	}

	// 6. Administrative / policy block. DSN x.7.x is a security/policy class; if we got
	//    here it wasn't spam or reputation, so it's a generic policy block.
	if subject == "7" {
		return CategoryBlock
	}
	if containsAny(text, blockPatterns) {
		return CategoryBlock
	}

	// 7. Fallback on permanence only. Prefer the enhanced class (RFC 3463: 4 = persistent
	//    transient, 5 = permanent) and fall back to the SMTP reply code.
	switch {
	case class == "5" || smtpCode >= 500:
		return CategoryHard
	case class == "4" || (smtpCode >= 400 && smtpCode < 500):
		return CategorySoft
	default:
		return CategoryUnknown
	}
}

// enhancedParts splits an RFC 3463 enhanced status like "5.1.1" into ("5","1","1").
// Returns empty strings for a malformed or absent code.
func enhancedParts(code string) (class, subject, detail string) {
	code = strings.TrimSpace(code)
	if code == "" {
		return "", "", ""
	}
	parts := strings.Split(code, ".")
	if len(parts) != 3 {
		return "", "", ""
	}
	return parts[0], parts[1], parts[2]
}

func containsAny(haystack string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(haystack, n) {
			return true
		}
	}
	return false
}
