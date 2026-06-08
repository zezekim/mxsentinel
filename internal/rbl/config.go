package rbl

import (
	"os"
	"strings"
	"time"
)

// DefaultZones is the built-in set of DNSBL zones checked when RBL_ZONES is unset.
var DefaultZones = []string{
	"zen.spamhaus.org",
	"b.barracudacentral.org",
	"bl.spamcop.net",
	"dnsbl.sorbs.net",
	"cbl.abuseat.org",
}

// DefaultInterval is the tick interval when RBL_INTERVAL is unset/invalid.
const DefaultInterval = 15 * time.Minute

// DefaultHealthyIPsFile is where the healthy-IP set is written when RBL_HEALTHY_IPS_FILE
// is unset. A host-side hook (cron) reads this to rebuild the Postfix randmap source.
const DefaultHealthyIPsFile = "/var/lib/mxsentinel/healthy-ips"

// Config holds rbld's runtime configuration, resolved from environment variables with safe
// defaults. It never fabricates egress IPs: if none are supplied the IP list is empty and
// the daemon logs/serves an empty (degenerate) state rather than guessing.
type Config struct {
	EgressIPs          []string      // RELAY_EGRESS_IPS (fallback RELAY_NODE_IP)
	Zones              []string      // RBL_ZONES
	Interval           time.Duration // RBL_INTERVAL
	HealthyIPsFile     string        // RBL_HEALTHY_IPS_FILE
	UsedNodeIPFallback bool          // true when EgressIPs came from RELAY_NODE_IP
	LookupTimeout      time.Duration // RBL_LOOKUP_TIMEOUT (per-lookup), default 5s
}

// LoadConfig reads rbld's configuration from the environment.
func LoadConfig() Config {
	c := Config{
		Zones:          splitCSV(getenv("RBL_ZONES", "")),
		Interval:       parseDuration("RBL_INTERVAL", DefaultInterval),
		HealthyIPsFile: getenv("RBL_HEALTHY_IPS_FILE", DefaultHealthyIPsFile),
		LookupTimeout:  parseDuration("RBL_LOOKUP_TIMEOUT", 5*time.Second),
	}
	if len(c.Zones) == 0 {
		c.Zones = append([]string(nil), DefaultZones...)
	}

	egress := splitCSV(getenv("RELAY_EGRESS_IPS", ""))
	if len(egress) == 0 {
		if node := strings.TrimSpace(os.Getenv("RELAY_NODE_IP")); node != "" {
			egress = []string{node}
			c.UsedNodeIPFallback = true
		}
	}
	c.EgressIPs = dedupe(egress)
	return c
}

func getenv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return v
	}
	return def
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

func dedupe(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	var out []string
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
