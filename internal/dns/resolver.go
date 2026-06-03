// Package dns is the DNS Intelligence engine: it resolves and validates a domain's mail
// authentication and infrastructure records (SPF/DKIM/DMARC/MX/MTA-STS/...), producing a
// versioned snapshot plus findings. See docs/phase-1-plan.md WS3 and ARCHITECTURE.md §2.
package dns

import (
	"context"
	"errors"
	"net"
	"time"
)

// Resolver abstracts DNS lookups so validators can run against live DNS (SystemResolver)
// or fixtures (StaticResolver). "Not found" (NXDOMAIN / no records) is normalized to an
// empty result with a nil error; only genuine failures (timeout, SERVFAIL) return errors.
//
// We use the standard library net.Resolver for the standard record types, which is
// sufficient for SPF/DKIM/DMARC/MX. A miekg/dns-backed resolver can be swapped in later
// behind this interface for DNSSEC / raw-record needs (see docs/tech-stack.md).
type Resolver interface {
	TXT(ctx context.Context, name string) ([]string, error)
	MX(ctx context.Context, name string) ([]*net.MX, error)
	IP(ctx context.Context, host string) ([]net.IP, error)
	PTR(ctx context.Context, addr string) ([]string, error)
}

// SystemResolver resolves against the host's configured DNS.
type SystemResolver struct {
	r       *net.Resolver
	timeout time.Duration
}

// NewSystemResolver returns a resolver using the system DNS config with a per-lookup
// timeout (default 5s if zero).
func NewSystemResolver(timeout time.Duration) *SystemResolver {
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	return &SystemResolver{r: net.DefaultResolver, timeout: timeout}
}

// NewResolverWithServer returns a resolver that sends every query to a specific DNS
// server (e.g. "1.1.1.1" or "9.9.9.9:53") instead of the host's configured resolver. A
// bare address gets the default DNS port (53). timeout defaults to 5s if zero.
func NewResolverWithServer(server string, timeout time.Duration) *SystemResolver {
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	addr := withDefaultDNSPort(server)
	d := net.Dialer{Timeout: timeout}
	r := &net.Resolver{
		PreferGo: true, // use Go's built-in DNS client so Dial is honored
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return d.DialContext(ctx, network, addr)
		},
	}
	return &SystemResolver{r: r, timeout: timeout}
}

// ResolverFor builds a resolver from tenant-configured settings: a custom server when
// address is non-empty, otherwise the host's system resolver. timeoutSecs <= 0 means the
// 5s default.
func ResolverFor(address string, timeoutSecs int) Resolver {
	timeout := time.Duration(timeoutSecs) * time.Second
	if address == "" {
		return NewSystemResolver(timeout)
	}
	return NewResolverWithServer(address, timeout)
}

// withDefaultDNSPort appends :53 to a bare host/IP. host:port values pass through.
func withDefaultDNSPort(server string) string {
	if _, _, err := net.SplitHostPort(server); err == nil {
		return server
	}
	return net.JoinHostPort(server, "53")
}

func (s *SystemResolver) ctx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, s.timeout)
}

func (s *SystemResolver) TXT(ctx context.Context, name string) ([]string, error) {
	c, cancel := s.ctx(ctx)
	defer cancel()
	recs, err := s.r.LookupTXT(c, name)
	return emptyIfNotFound(recs, err)
}

func (s *SystemResolver) MX(ctx context.Context, name string) ([]*net.MX, error) {
	c, cancel := s.ctx(ctx)
	defer cancel()
	recs, err := s.r.LookupMX(c, name)
	return emptyIfNotFound(recs, err)
}

func (s *SystemResolver) IP(ctx context.Context, host string) ([]net.IP, error) {
	c, cancel := s.ctx(ctx)
	defer cancel()
	addrs, err := s.r.LookupIPAddr(c, host)
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		ips = append(ips, a.IP)
	}
	return ips, nil
}

func (s *SystemResolver) PTR(ctx context.Context, addr string) ([]string, error) {
	c, cancel := s.ctx(ctx)
	defer cancel()
	names, err := s.r.LookupAddr(c, addr)
	return emptyIfNotFound(names, err)
}

// emptyIfNotFound collapses NXDOMAIN/no-record errors into an empty, nil-error result.
func emptyIfNotFound[T any](v []T, err error) ([]T, error) {
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return v, nil
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	var de *net.DNSError
	if errors.As(err, &de) {
		return de.IsNotFound
	}
	return false
}

// StaticResolver is a fixture-backed resolver for tests. Unset names resolve to empty
// (no record). A name present in Errors returns that error instead.
type StaticResolver struct {
	TXTRecords map[string][]string
	MXRecords  map[string][]*net.MX
	IPRecords  map[string][]net.IP
	PTRRecords map[string][]string
	Errors     map[string]error
}

// NewStaticResolver returns an empty StaticResolver with initialized maps.
func NewStaticResolver() *StaticResolver {
	return &StaticResolver{
		TXTRecords: map[string][]string{},
		MXRecords:  map[string][]*net.MX{},
		IPRecords:  map[string][]net.IP{},
		PTRRecords: map[string][]string{},
		Errors:     map[string]error{},
	}
}

func (s *StaticResolver) TXT(_ context.Context, name string) ([]string, error) {
	if err := s.Errors[name]; err != nil {
		return nil, err
	}
	return s.TXTRecords[name], nil
}

func (s *StaticResolver) MX(_ context.Context, name string) ([]*net.MX, error) {
	if err := s.Errors[name]; err != nil {
		return nil, err
	}
	return s.MXRecords[name], nil
}

func (s *StaticResolver) IP(_ context.Context, host string) ([]net.IP, error) {
	if err := s.Errors[host]; err != nil {
		return nil, err
	}
	return s.IPRecords[host], nil
}

func (s *StaticResolver) PTR(_ context.Context, addr string) ([]string, error) {
	if err := s.Errors[addr]; err != nil {
		return nil, err
	}
	return s.PTRRecords[addr], nil
}
