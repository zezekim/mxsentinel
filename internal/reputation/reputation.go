// Package reputation checks sending IPs against DNS blocklists (DNSBLs/RBLs). It reuses
// the DNS resolver from internal/dns, so the logic is pure and testable with fixtures.
// cmd/repd polls registered IPs and emits reputation.blacklist_hit events on listings.
package reputation

import (
	"context"
	"fmt"
	"net"
	"strings"

	dnsx "github.com/zezekim/mxsentinel/internal/dns"
)

// DefaultZones is a starter set of public DNSBL zones. Operators can override per
// deployment.
var DefaultZones = []string{
	"zen.spamhaus.org",
	"bl.spamcop.net",
	"b.barracudacentral.org",
	"dnsbl.sorbs.net",
}

// Listing is a positive DNSBL hit for one zone.
type Listing struct {
	Zone  string
	Codes []string // the 127.0.0.x A records the zone returned
	TXT   []string // optional human-readable reason(s)
}

// ReverseIPv4 returns the reversed-octet label used for DNSBL queries
// (1.2.3.4 -> "4.3.2.1"). ok is false for non-IPv4 addresses.
func ReverseIPv4(ip net.IP) (string, bool) {
	v4 := ip.To4()
	if v4 == nil {
		return "", false
	}
	return fmt.Sprintf("%d.%d.%d.%d", v4[3], v4[2], v4[1], v4[0]), true
}

// Check queries each zone for ip and returns the zones that list it. A zone "lists" the IP
// when the query <reversed-ip>.<zone> resolves to an address in 127.0.0.0/8 (the DNSBL
// convention). Lookup errors are treated as not-listed (best effort). IPv6 is not yet
// supported and yields no listings.
func Check(ctx context.Context, r dnsx.Resolver, ip string, zones []string) ([]Listing, error) {
	parsed := net.ParseIP(strings.TrimSpace(ip))
	if parsed == nil {
		return nil, fmt.Errorf("invalid ip: %q", ip)
	}
	rev, ok := ReverseIPv4(parsed)
	if !ok {
		return nil, nil // IPv6 unsupported for now
	}

	var listings []Listing
	for _, zone := range zones {
		name := rev + "." + zone
		addrs, err := r.IP(ctx, name)
		if err != nil || len(addrs) == 0 {
			continue // lookup error or NXDOMAIN => not listed
		}
		var codes []string
		for _, a := range addrs {
			if isDNSBLHit(a) {
				codes = append(codes, a.String())
			}
		}
		if len(codes) == 0 {
			continue
		}
		l := Listing{Zone: zone, Codes: codes}
		if txts, terr := r.TXT(ctx, name); terr == nil && len(txts) > 0 {
			l.TXT = txts
		}
		listings = append(listings, l)
	}
	return listings, nil
}

// isDNSBLHit reports whether an answer falls in 127.0.0.0/8.
func isDNSBLHit(ip net.IP) bool {
	v4 := ip.To4()
	return v4 != nil && v4[0] == 127
}
