package reputation

import (
	"context"
	"net"
	"testing"

	dnsx "github.com/zezekim/mxsentinel/internal/dns"
)

func TestReverseIPv4(t *testing.T) {
	rev, ok := ReverseIPv4(net.ParseIP("1.2.3.4"))
	if !ok || rev != "4.3.2.1" {
		t.Errorf("ReverseIPv4 = %q,%v; want 4.3.2.1,true", rev, ok)
	}
	if _, ok := ReverseIPv4(net.ParseIP("2001:db8::1")); ok {
		t.Error("IPv6 should not be reversible (yet)")
	}
}

func TestCheckListed(t *testing.T) {
	r := dnsx.NewStaticResolver()
	// IP 198.51.100.5 listed on zen with code 127.0.0.2.
	r.IPRecords["5.100.51.198.zen.spamhaus.org"] = []net.IP{net.ParseIP("127.0.0.2")}
	r.TXTRecords["5.100.51.198.zen.spamhaus.org"] = []string{"https://www.spamhaus.org/sbl/query/SBL123"}

	listings, err := Check(context.Background(), r, "198.51.100.5", []string{"zen.spamhaus.org", "bl.spamcop.net"})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(listings) != 1 {
		t.Fatalf("expected 1 listing, got %d", len(listings))
	}
	if listings[0].Zone != "zen.spamhaus.org" || len(listings[0].Codes) != 1 || listings[0].Codes[0] != "127.0.0.2" {
		t.Errorf("unexpected listing: %+v", listings[0])
	}
	if len(listings[0].TXT) == 0 {
		t.Error("expected a TXT reason")
	}
}

func TestCheckNotListed(t *testing.T) {
	r := dnsx.NewStaticResolver()
	listings, err := Check(context.Background(), r, "203.0.113.9", DefaultZones)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(listings) != 0 {
		t.Errorf("expected no listings, got %d", len(listings))
	}
}

func TestCheckNonLoopbackAnswerIgnored(t *testing.T) {
	r := dnsx.NewStaticResolver()
	// Some resolvers return a non-127.x answer (e.g. a wildcard) which must NOT count.
	r.IPRecords["9.113.0.203.dnsbl.sorbs.net"] = []net.IP{net.ParseIP("203.0.113.9")}
	listings, err := Check(context.Background(), r, "203.0.113.9", []string{"dnsbl.sorbs.net"})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(listings) != 0 {
		t.Errorf("non-127.0.0.0/8 answer should not be a listing, got %+v", listings)
	}
}

func TestCheckInvalidIP(t *testing.T) {
	if _, err := Check(context.Background(), dnsx.NewStaticResolver(), "not-an-ip", DefaultZones); err == nil {
		t.Error("expected error for invalid IP")
	}
}

func TestCheckIPv6Skipped(t *testing.T) {
	listings, err := Check(context.Background(), dnsx.NewStaticResolver(), "2001:db8::1", DefaultZones)
	if err != nil || listings != nil {
		t.Errorf("IPv6 should be skipped with no error; got %+v, %v", listings, err)
	}
}
