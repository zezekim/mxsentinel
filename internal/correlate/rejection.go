// Package correlate is the Phase 2 intelligence core: it classifies SMTP rejections,
// detects rejection spikes, and correlates spikes against recent DNS changes to produce a
// root-cause hypothesis ("correlation is the moat" — see ARCHITECTURE.md §4 and the
// development phases). All logic here is pure and deterministic; the streaming wiring
// lives in cmd/correld.
package correlate

import "strings"

// ReasonCategory is a normalized reason a message was rejected or deferred. It refines the
// coarse bounce_class with provider-aware heuristics.
type ReasonCategory string

const (
	ReasonReputation  ReasonCategory = "reputation"   // sender/IP reputation
	ReasonBlocklist   ReasonCategory = "blocklist"    // listed on an RBL / blocked
	ReasonAuth        ReasonCategory = "auth"         // SPF/DKIM/DMARC failure or alignment
	ReasonSpamContent ReasonCategory = "spam_content" // content classified as spam
	ReasonRateLimit   ReasonCategory = "rate_limit"   // too many connections/messages
	ReasonGreylist    ReasonCategory = "greylist"     // temporary greylisting
	ReasonPolicy      ReasonCategory = "policy"       // recipient policy rejection
	ReasonUserUnknown ReasonCategory = "user_unknown" // no such mailbox
	ReasonMailboxFull ReasonCategory = "mailbox_full" // over quota
	ReasonConnection  ReasonCategory = "connection"   // connectivity/timeout
	ReasonTLS         ReasonCategory = "tls"          // TLS negotiation/cert
	ReasonUnknown     ReasonCategory = "unknown"
)

// Rejection is a classified delivery failure.
type Rejection struct {
	Category  ReasonCategory
	Code      int
	Enhanced  string
	Transient bool // 4xx (retryable) vs 5xx (permanent)
}

// ClassifyRejection maps an SMTP response to a normalized reason. It combines the SMTP
// code, the RFC 3463 enhanced status, the (lower-cased) free-text response, and
// provider-specific patterns. responseText is matched case-insensitively.
func ClassifyRejection(code int, enhanced, responseText, provider string) Rejection {
	r := Rejection{Code: code, Enhanced: enhanced, Transient: code >= 400 && code < 500}
	text := strings.ToLower(responseText)

	// Enhanced status gives strong signals (RFC 3463): X.7.x = security/policy.
	switch {
	case strings.HasPrefix(enhanced, "5.7.1") || strings.HasPrefix(enhanced, "4.7.1"):
		// 5.7.1 is overloaded: policy/auth/reputation. Fall through to text matching.
	case strings.HasPrefix(enhanced, "5.1.1") || strings.HasPrefix(enhanced, "5.1.0"):
		r.Category = ReasonUserUnknown
		return r
	case strings.HasPrefix(enhanced, "4.2.2") || strings.HasPrefix(enhanced, "5.2.2"):
		r.Category = ReasonMailboxFull
		return r
	}

	switch {
	case containsAny(text, "spf", "dkim", "dmarc", "domainkeys", "sender id", "not authorized", "authentication failed", "failed authentication"):
		r.Category = ReasonAuth
	case containsAny(text, "spamhaus", "spamcop", "barracuda", "blocklist", "blacklist", "blocked using", "rbl", "dnsbl", "on a block list", "on a blocklist"):
		r.Category = ReasonBlocklist
	case containsAny(text, "reputation", "poor reputation", "ip reputation", "sender reputation", "unsolicited", "complaints", "complaint rate"):
		r.Category = ReasonReputation
	case containsAny(text, "spam", "bulk", "junk", "content", "phishing", "malware", "virus"):
		r.Category = ReasonSpamContent
	// greylist is checked before rate-limit because greylisting messages often also say
	// "try again later".
	case containsAny(text, "greylist", "grey list", "graylist", "deferred. please try", "temporarily deferred"):
		r.Category = ReasonGreylist
	case containsAny(text, "rate limit", "rate limited", "too many", "throttl", "421 4.7.0", "try again later", "connection limit", "exceeded the maximum"):
		r.Category = ReasonRateLimit
	case containsAny(text, "user unknown", "no such user", "recipient not found", "mailbox unavailable", "does not exist", "invalid recipient", "address rejected"):
		r.Category = ReasonUserUnknown
	case containsAny(text, "quota", "mailbox full", "over quota", "insufficient storage"):
		r.Category = ReasonMailboxFull
	case containsAny(text, "tls", "starttls", "certificate", "cipher"):
		r.Category = ReasonTLS
	case containsAny(text, "policy", "rejected due to policy", "message rejected", "not accepted"):
		r.Category = ReasonPolicy
	case containsAny(text, "timed out", "timeout", "connection refused", "no route", "could not connect", "connection reset"):
		r.Category = ReasonConnection
	default:
		r.Category = classifyByCode(code, r.Transient)
	}

	// Provider-specific overrides for well-known opaque codes.
	switch strings.ToLower(provider) {
	case "google":
		if strings.Contains(text, "5.7.26") || strings.Contains(text, "unauthenticated email") {
			r.Category = ReasonAuth
		}
		if strings.Contains(text, "5.7.1") && strings.Contains(text, "blocked") {
			r.Category = ReasonReputation
		}
	case "microsoft":
		// Outlook spam/reputation block markers.
		if containsAny(text, "s3140", "s3150", "550 5.7.1 unfortunately", "high probability of spam") {
			r.Category = ReasonReputation
		}
		if containsAny(text, "access denied", "banned sending ip") {
			r.Category = ReasonBlocklist
		}
	case "yahoo":
		if containsAny(text, "[ts03]", "[ts04]", "temporarily deferred due to user complaints") {
			r.Category = ReasonReputation
		}
	}

	return r
}

// classifyByCode falls back to a coarse category from the SMTP code alone.
func classifyByCode(code int, transient bool) ReasonCategory {
	switch code {
	case 421:
		return ReasonRateLimit
	case 450, 451, 452:
		return ReasonGreylist
	case 550:
		return ReasonPolicy
	case 552:
		return ReasonMailboxFull
	case 554:
		return ReasonPolicy
	}
	if transient {
		return ReasonConnection
	}
	return ReasonUnknown
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
