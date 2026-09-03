package api

import (
	"net"
	"net/http"
	"strings"
)

// clientIP returns the caller's source address for audit trails and login notifications.
//
// apid never faces the internet directly — Caddy proxies to it on 127.0.0.1 — and a
// deployment may put Cloudflare in front of Caddy for some hostnames (this one fronts
// control.mxsentinel.app). Each hop appends itself to X-Forwarded-For, so by the time the
// request lands here the left-most XFF entry is only the real client if every proxy in the
// chain preserved it; a Cloudflare-proxied request can arrive with the edge address there
// instead. Cloudflare's own CF-Connecting-IP is set at the edge and always holds the true
// client address, so it wins when present.
//
// Order: CF-Connecting-IP → True-Client-IP (Cloudflare Enterprise / Akamai) → left-most
// X-Forwarded-For → RemoteAddr. Entries that are not a valid IP are skipped rather than
// reported verbatim.
//
// Trust boundary: these headers are only as good as the proxy chain. That is acceptable
// here — the value is displayed in an audit trail, never used for authorization — and it
// is why apid must stay bound to 127.0.0.1, reachable only through Caddy.
func clientIP(r *http.Request) string {
	for _, h := range []string{"CF-Connecting-IP", "True-Client-IP"} {
		if ip := parseIP(r.Header.Get(h)); ip != "" {
			return ip
		}
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		for _, hop := range strings.Split(xff, ",") {
			if ip := parseIP(hop); ip != "" {
				return ip
			}
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// parseIP returns the trimmed value if it is a valid IP address, else "".
func parseIP(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || net.ParseIP(v) == nil {
		return ""
	}
	return v
}

// clientCountry returns the ISO-3166-1 alpha-2 country Cloudflare resolved for the caller,
// or "" when the request did not come through Cloudflare. "XX" (unknown) and "T1" (Tor)
// are passed through as-is — both are meaningful in a "was that login me?" glance.
func clientCountry(r *http.Request) string {
	c := strings.ToUpper(strings.TrimSpace(r.Header.Get("CF-IPCountry")))
	// Two characters, opening with a letter: "US", "XX" (unknown) and "T1" (Tor) all pass,
	// anything else is treated as absent rather than echoed into a notification.
	if len(c) != 2 || c[0] < 'A' || c[0] > 'Z' {
		return ""
	}
	if !(c[1] >= 'A' && c[1] <= 'Z') && !(c[1] >= '0' && c[1] <= '9') {
		return ""
	}
	return c
}
