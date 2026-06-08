package authwatch

import "context"

// GeoLookup resolves a submitting client's source IP to a coarse location/ASN so the
// detector can flag a credential authenticating from a brand-new geo or network — the
// classic credential-compromise tell.
//
// It is an INTERFACE WITH A NO-OP DEFAULT (NoopGeo) on purpose. In MX Sentinel's current
// shared-relay topology the submitting client's source IP is NOT on the event bus:
// SMTPPayload.RelayIP is the EGRESS node IP, and all submission shares one source IP (the
// hosting server). Implementing a real geo/ASN anomaly check therefore needs TWO follow-ups:
//
//  1. a telemetry extension (cmd/telemetryd) to capture the smtpd CLIENT IP per SASL login
//     and put it on the bus (e.g. SMTPPayload.SourceIP populated for submissions), and
//  2. a GeoIP database (e.g. MaxMind GeoLite2) provisioned host-side.
//
// Until then the detector calls Lookup but the no-op returns ok=false, so the geo path is
// inert and never fabricates a location. See the daemon's caveats.
type GeoLookup interface {
	// Lookup maps an IP to a stable location key (e.g. "US/AS13335") and asn. ok=false when
	// the IP is empty/unresolvable or geo lookup is disabled.
	Lookup(ctx context.Context, ip string) (locationKey string, asn string, ok bool)
}

// NoopGeo is the default GeoLookup: it always reports ok=false, disabling the geo path. This
// keeps the MVP honest on a shared relay where the client source IP is unavailable.
type NoopGeo struct{}

// Lookup always returns ok=false.
func (NoopGeo) Lookup(context.Context, string) (string, string, bool) { return "", "", false }
