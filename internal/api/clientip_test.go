package api

import (
	"net/http"
	"testing"
)

func req(headers map[string]string, remote string) *http.Request {
	r, _ := http.NewRequest("GET", "/", nil)
	r.RemoteAddr = remote
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

func TestClientIP(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
		remote  string
		want    string
	}{
		{
			// The case this exists for: Cloudflare fronts the hostname, so by the time the
			// request reaches apid the left-most XFF entry is an edge address, not the user.
			name: "cloudflare header wins over XFF",
			headers: map[string]string{
				"CF-Connecting-IP": "203.0.113.7",
				"X-Forwarded-For":  "172.71.0.9, 127.0.0.1",
			},
			remote: "127.0.0.1:5000",
			want:   "203.0.113.7",
		},
		{
			name:    "true-client-ip is the fallback edge header",
			headers: map[string]string{"True-Client-IP": "203.0.113.8", "X-Forwarded-For": "172.71.0.9"},
			remote:  "127.0.0.1:5000",
			want:    "203.0.113.8",
		},
		{
			name:    "plain caddy: left-most XFF",
			headers: map[string]string{"X-Forwarded-For": "198.51.100.4, 127.0.0.1"},
			remote:  "127.0.0.1:5000",
			want:    "198.51.100.4",
		},
		{
			name:    "single XFF entry",
			headers: map[string]string{"X-Forwarded-For": "198.51.100.4"},
			remote:  "127.0.0.1:5000",
			want:    "198.51.100.4",
		},
		{
			name:    "IPv6 survives intact",
			headers: map[string]string{"CF-Connecting-IP": "2606:4700:4700::1111"},
			remote:  "127.0.0.1:5000",
			want:    "2606:4700:4700::1111",
		},
		{
			// A junk header must not shadow the usable value behind it.
			name:    "garbage header is skipped",
			headers: map[string]string{"CF-Connecting-IP": "unknown", "X-Forwarded-For": "198.51.100.4"},
			remote:  "127.0.0.1:5000",
			want:    "198.51.100.4",
		},
		{
			name:    "junk XFF hop is skipped",
			headers: map[string]string{"X-Forwarded-For": "hidden, 198.51.100.4"},
			remote:  "127.0.0.1:5000",
			want:    "198.51.100.4",
		},
		{
			name:    "no proxy headers: RemoteAddr without port",
			headers: nil,
			remote:  "192.0.2.10:41234",
			want:    "192.0.2.10",
		},
		{
			name:    "unparseable RemoteAddr is returned as-is",
			headers: nil,
			remote:  "weird",
			want:    "weird",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := clientIP(req(tc.headers, tc.remote)); got != tc.want {
				t.Errorf("clientIP() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestClientCountry(t *testing.T) {
	tests := map[string]string{
		"US":  "US",
		"us":  "US",
		"XX":  "XX", // Cloudflare's "unknown"
		"T1":  "T1", // Cloudflare's Tor pseudo-country
		"":    "",
		"USA": "",
		"1":   "",
		"1A":  "", // must open with a letter
	}
	for header, want := range tests {
		h := map[string]string{"CF-IPCountry": header}
		if got := clientCountry(req(h, "127.0.0.1:1")); got != want {
			t.Errorf("clientCountry(%q) = %q, want %q", header, got, want)
		}
	}
	// No Cloudflare in front: header absent entirely.
	if got := clientCountry(req(nil, "127.0.0.1:1")); got != "" {
		t.Errorf("clientCountry(absent) = %q, want empty", got)
	}
}
