// Package mtasts validates a domain's MTA-STS (RFC 8461) transport-security posture:
// it resolves the _mta-sts TXT record, fetches and parses the published policy
// (https://mta-sts.<domain>/.well-known/mta-sts.txt), and checks the TLS certificates of
// the policy's MX hosts for validity and expiry. Parsing is pure and network-free so it
// can be unit-tested against fixture strings; live lookups happen behind the Resolver,
// PolicyFetcher and CertChecker interfaces.
//
// See docs/tls-reporting.md and ARCHITECTURE.md (transport-security layer).
package mtasts

import (
	"fmt"
	"strconv"
	"strings"
)

// Mode is the MTA-STS enforcement mode from a published policy.
type Mode string

const (
	ModeNone    Mode = "none"    // policy exists but requests no enforcement
	ModeTesting Mode = "testing" // report failures but still deliver
	ModeEnforce Mode = "enforce" // refuse delivery on TLS failure
)

// ValidMode reports whether m is one of the RFC 8461 modes.
func ValidMode(m Mode) bool {
	switch m {
	case ModeNone, ModeTesting, ModeEnforce:
		return true
	default:
		return false
	}
}

// Policy is a parsed MTA-STS policy file.
type Policy struct {
	Version string   // must be "STSv1"
	Mode    Mode     // none|testing|enforce
	MX      []string // allowed MX host patterns (may include a leading "*.")
	MaxAge  int      // seconds the policy may be cached
}

// STSRecord is the parsed _mta-sts.<domain> TXT record (e.g. "v=STSv1; id=20240101T000000Z").
type STSRecord struct {
	Version string // must be "STSv1"
	ID      string // opaque policy id; a change signals a new policy
}

// ParsePolicy parses an MTA-STS policy file body (RFC 8461 §3.2). The format is a set of
// CRLF-delimited "key: value" lines; the "mx" key may repeat. It is strict about the
// version and mode but tolerant of unknown keys (ignored) so future extensions don't break
// ingestion.
func ParsePolicy(body string) (Policy, error) {
	var p Policy
	seenVersion := false
	for _, raw := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			return Policy{}, fmt.Errorf("mtasts: malformed policy line %q", line)
		}
		key = strings.ToLower(strings.TrimSpace(key))
		val = strings.TrimSpace(val)
		switch key {
		case "version":
			p.Version = val
			seenVersion = true
		case "mode":
			p.Mode = Mode(strings.ToLower(val))
		case "mx":
			if val != "" {
				p.MX = append(p.MX, strings.ToLower(val))
			}
		case "max_age":
			n, err := strconv.Atoi(val)
			if err != nil {
				return Policy{}, fmt.Errorf("mtasts: invalid max_age %q: %w", val, err)
			}
			p.MaxAge = n
		default:
			// Unknown keys are ignored per the extensibility rule.
		}
	}
	if !seenVersion || !strings.EqualFold(p.Version, "STSv1") {
		return Policy{}, fmt.Errorf("mtasts: missing or unsupported version %q", p.Version)
	}
	if !ValidMode(p.Mode) {
		return Policy{}, fmt.Errorf("mtasts: invalid mode %q", p.Mode)
	}
	if p.Mode != ModeNone && len(p.MX) == 0 {
		return Policy{}, fmt.Errorf("mtasts: policy in mode %q lists no mx hosts", p.Mode)
	}
	return p, nil
}

// ParseTXT parses the _mta-sts.<domain> TXT record (RFC 8461 §3.1), a set of ";"-separated
// "k=v" pairs. The version must be STSv1 and an id is required.
func ParseTXT(txt string) (STSRecord, error) {
	var rec STSRecord
	for _, part := range strings.Split(txt, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, val, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "v":
			rec.Version = strings.TrimSpace(val)
		case "id":
			rec.ID = strings.TrimSpace(val)
		}
	}
	if !strings.EqualFold(rec.Version, "STSv1") {
		return STSRecord{}, fmt.Errorf("mtasts: TXT record missing v=STSv1 (got %q)", rec.Version)
	}
	if rec.ID == "" {
		return STSRecord{}, fmt.Errorf("mtasts: TXT record missing id")
	}
	return rec, nil
}
