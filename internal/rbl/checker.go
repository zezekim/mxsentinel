// Package rbl implements RBL/DNSBL self-monitoring for the relay's own egress IPs.
//
// For each configured egress IP and each DNSBL zone it performs a reversed-octet A lookup
// against the zone: e.g. checking 1.2.3.4 on zen.spamhaus.org resolves 4.3.2.1.zen.spamhaus.org.
// Any A answer means the IP is LISTED on that zone; a follow-up TXT lookup gives the reason
// (typically a delisting URL or cause). The result is upserted into ip_blocklist_status and,
// on a clean->listed transition, a critical incident is opened (see cmd/rbld).
//
// DNSBL semantics: by convention a positive listing returns an A record in 127.0.0.0/8 (the
// last octet encodes the list/reason). We treat ANY successful A answer as "listed" and an
// NXDOMAIN / no-answer as "not listed". DNS lookup errors (timeout, SERVFAIL) are reported
// as errors so the daemon can avoid mistaking an outage for a clean result — a transient
// error never clears or sets a listing.
package rbl

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// Listing is the result of checking one egress IP against one DNSBL zone.
type Listing struct {
	IP     string
	Zone   string
	Listed bool
	Reason string // TXT answer when listed; "" otherwise
	// Codes holds the raw A answers (usually 127.0.0.x) for diagnostics.
	Codes []string
}

// Checker performs reversed-octet DNSBL lookups using a net.Resolver with a per-lookup
// timeout. The zero value is not usable; construct with NewChecker.
type Checker struct {
	resolver *net.Resolver
	timeout  time.Duration
}

// NewChecker returns a Checker that uses the system resolver (or a custom one if non-nil)
// and applies the given per-lookup timeout. A non-positive timeout defaults to 5s.
func NewChecker(resolver *net.Resolver, perLookupTimeout time.Duration) *Checker {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	if perLookupTimeout <= 0 {
		perLookupTimeout = 5 * time.Second
	}
	return &Checker{resolver: resolver, timeout: perLookupTimeout}
}

// reverseIPv4 turns "1.2.3.4" into "4.3.2.1". It returns an error for anything that is not
// a dotted-quad IPv4 address (DNSBLs are overwhelmingly IPv4; IPv6 nibble-reversal against
// these zones is rarely supported and is intentionally out of scope here).
func reverseIPv4(ip string) (string, error) {
	parsed := net.ParseIP(strings.TrimSpace(ip))
	if parsed == nil {
		return "", fmt.Errorf("invalid IP %q", ip)
	}
	v4 := parsed.To4()
	if v4 == nil {
		return "", fmt.Errorf("not an IPv4 address: %q (DNSBL checks are IPv4-only)", ip)
	}
	return fmt.Sprintf("%d.%d.%d.%d", v4[3], v4[2], v4[1], v4[0]), nil
}

// queryName builds the reversed-octet DNSBL query name for an IP on a zone.
func queryName(ip, zone string) (string, error) {
	rev, err := reverseIPv4(ip)
	if err != nil {
		return "", err
	}
	return rev + "." + strings.TrimSuffix(strings.TrimSpace(zone), "."), nil
}

// Check looks up one egress IP against one DNSBL zone. A returned error means the lookup
// could not be completed reliably (timeout/SERVFAIL/invalid IP); the caller MUST NOT treat
// that as either listed or clean. NXDOMAIN / "no such host" is a definitive "not listed"
// (Listed=false, nil error).
func (c *Checker) Check(ctx context.Context, ip, zone string) (Listing, error) {
	res := Listing{IP: ip, Zone: zone}
	name, err := queryName(ip, zone)
	if err != nil {
		return res, err
	}

	aCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	addrs, err := c.resolver.LookupHost(aCtx, name)
	if err != nil {
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) && (dnsErr.IsNotFound) {
			// NXDOMAIN / no host: a definitive "not listed".
			return res, nil
		}
		// Timeout, SERVFAIL, network error: indeterminate — surface it.
		return res, fmt.Errorf("A lookup %s: %w", name, err)
	}
	if len(addrs) == 0 {
		return res, nil // no answer: not listed
	}

	res.Listed = true
	res.Codes = addrs

	// TXT lookup for the reason. A missing/failing TXT is non-fatal — keep the listing.
	txtCtx, txtCancel := context.WithTimeout(ctx, c.timeout)
	defer txtCancel()
	if txts, terr := c.resolver.LookupTXT(txtCtx, name); terr == nil && len(txts) > 0 {
		res.Reason = strings.Join(txts, " ")
	}
	if res.Reason == "" {
		res.Reason = "listed (no TXT reason published; codes: " + strings.Join(addrs, ",") + ")"
	}
	return res, nil
}
