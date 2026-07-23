package smtpprobe

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// DefaultInterval is how often probed re-probes every endpoint when MXS_PROBE_INTERVAL is
// unset/invalid.
const DefaultInterval = 60 * time.Second

// Config holds probed's runtime configuration, resolved from environment variables with safe
// defaults. Like internal/rbl it never fabricates targets: if none are supplied the endpoint
// list is empty and the daemon/API serve a degenerate (empty) state rather than guessing.
//
// Environment:
//
//	MXS_PROBE_ENDPOINTS   comma-separated "host:port[:mode]" targets, e.g.
//	                      "relay.example.com:587:starttls,relay.example.com:465:implicit_tls".
//	                      mode defaults from the port (25→plain, 587→starttls, 465→implicit_tls).
//	MXS_PROBE_HOST        relay hostname used when only ports are given (fallback RELAY_HOST /
//	                      RELAY_FQDN / RELAY_NODE_IP).
//	MXS_PROBE_PORTS       comma-separated ports to probe on MXS_PROBE_HOST (default "25,587,465").
//	MXS_PROBE_INTERVAL    probe cadence (default 60s).
//	MXS_PROBE_EHLO_NAME   name sent in EHLO (default "mxsentinel.probe").
//	MXS_PROBE_CERT_WARN   cert-expiry warning lead time (default 336h = 14d).
//	MXS_PROBE_CONNECT_TIMEOUT / MXS_PROBE_COMMAND_TIMEOUT  (defaults 10s / 10s).
//	MXS_PROBE_CHECK_RESPONSE  "1"/"true" to sample MAIL/RCPT for greylisting (default off).
//	MXS_PROBE_TLS_INSECURE     "1"/"true" to skip chain verification reporting (default off).
//	MXS_PROBE_TENANT_ID   tenant UUID stamped on emitted reputation events (fallback
//	                      RELAY_TENANT_ID); when empty probed records results but does not emit.
type Config struct {
	Endpoints         []Endpoint
	Interval          time.Duration
	EHLOName          string
	CertWarnThreshold time.Duration
	ConnectTimeout    time.Duration
	CommandTimeout    time.Duration
	CheckResponse     bool
	TLSInsecure       bool
	TenantID          string
}

// LoadConfig reads probed's configuration from the environment.
func LoadConfig() Config {
	c := Config{
		Interval:          parseDuration("MXS_PROBE_INTERVAL", DefaultInterval),
		EHLOName:          getenv("MXS_PROBE_EHLO_NAME", "mxsentinel.probe"),
		CertWarnThreshold: parseDuration("MXS_PROBE_CERT_WARN", DefaultCertWarnThreshold),
		ConnectTimeout:    parseDuration("MXS_PROBE_CONNECT_TIMEOUT", 10*time.Second),
		CommandTimeout:    parseDuration("MXS_PROBE_COMMAND_TIMEOUT", 10*time.Second),
		CheckResponse:     parseBool("MXS_PROBE_CHECK_RESPONSE"),
		TLSInsecure:       parseBool("MXS_PROBE_TLS_INSECURE"),
		TenantID:          firstNonEmpty(os.Getenv("MXS_PROBE_TENANT_ID"), os.Getenv("RELAY_TENANT_ID")),
	}
	c.Endpoints = loadEndpoints()
	return c
}

// loadEndpoints resolves the target endpoint list. It prefers the explicit
// MXS_PROBE_ENDPOINTS spec; failing that it expands MXS_PROBE_HOST × MXS_PROBE_PORTS.
func loadEndpoints() []Endpoint {
	if spec := strings.TrimSpace(os.Getenv("MXS_PROBE_ENDPOINTS")); spec != "" {
		return ParseEndpoints(spec)
	}
	host := firstNonEmpty(
		os.Getenv("MXS_PROBE_HOST"),
		os.Getenv("RELAY_HOST"),
		os.Getenv("RELAY_FQDN"),
		os.Getenv("RELAY_NODE_IP"),
	)
	if host == "" {
		return nil
	}
	ports := splitCSV(getenv("MXS_PROBE_PORTS", "25,587,465"))
	var eps []Endpoint
	for _, ps := range ports {
		if port, err := strconv.Atoi(strings.TrimSpace(ps)); err == nil && port > 0 {
			eps = append(eps, Endpoint{Host: host, Port: port, Mode: ModeForPort(port)})
		}
	}
	return dedupeEndpoints(eps)
}

// ParseEndpoints parses a comma-separated list of "host:port[:mode]" specs. Unparseable
// entries are skipped. Mode defaults from the port when omitted.
func ParseEndpoints(spec string) []Endpoint {
	var eps []Endpoint
	for _, raw := range strings.Split(spec, ",") {
		item := strings.TrimSpace(raw)
		if item == "" {
			continue
		}
		parts := strings.Split(item, ":")
		if len(parts) < 2 {
			continue
		}
		host := strings.TrimSpace(parts[0])
		port, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if host == "" || err != nil || port <= 0 {
			continue
		}
		mode := ModeForPort(port)
		if len(parts) >= 3 {
			if m := normalizeMode(parts[2]); m != "" {
				mode = m
			}
		}
		eps = append(eps, Endpoint{Host: host, Port: port, Mode: mode})
	}
	return dedupeEndpoints(eps)
}

// ModeForPort returns the conventional transport mode for a well-known SMTP port.
func ModeForPort(port int) Mode {
	switch port {
	case 465:
		return ModeImplicitTLS
	case 587:
		return ModeSTARTTLS
	default:
		return ModePlain
	}
}

func normalizeMode(s string) Mode {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "plain", "cleartext", "none":
		return ModePlain
	case "starttls", "tls":
		return ModeSTARTTLS
	case "implicit_tls", "implicit", "smtps", "tls_implicit":
		return ModeImplicitTLS
	default:
		return ""
	}
}

func dedupeEndpoints(in []Endpoint) []Endpoint {
	seen := make(map[string]struct{}, len(in))
	var out []Endpoint
	for _, e := range in {
		if _, ok := seen[e.Label()]; ok {
			continue
		}
		seen[e.Label()] = struct{}{}
		out = append(out, e)
	}
	return out
}

func getenv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return v
	}
	return def
}

func parseBool(key string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

func parseDuration(key string, def time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(strings.TrimSpace(v))
	if err != nil || d <= 0 {
		return def
	}
	return d
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}
