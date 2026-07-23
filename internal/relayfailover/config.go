// Package relayfailover implements a circuit breaker that reroutes a receiver provider's
// outbound mail to a fallback smarthost (e.g. mail.baby) when the relay's DIRECT delivery
// to that provider is sustainedly deferred with transient 4xx codes.
//
// Scope, deliberately narrow: only TRANSIENT 4xx defers trip the breaker. A persistent 5xx
// spam/reputation block is NOT a failover trigger — rerouting spam-blocked mail through a
// fallback relay just launders the same reputation problem onto the fallback's IPs (and
// violates most relay providers' terms). Fix those at the source (auth alignment, warmup,
// complaint rate) instead. See docs/relay-failover.md.
//
// The daemon (relayfailoverd) only ever writes a STATE FILE naming the recipient domains
// currently in failover; a host-side hook (deploy/hooks/relay-failover-hook.sh) reads it,
// rebuilds the Postfix transport-map overlay, and requeues deferred mail. MX Sentinel never
// touches the host mail path directly — mirrors the rbld/healthy-ips design — so the mail
// path keeps working even if this daemon is down (the map simply stops changing).
package relayfailover

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Default target: Microsoft/Outlook. The provider label matches internal/telemetry.Provider
// ("microsoft" covers outlook.com/hotmail/office365/protection.outlook).
const (
	DefaultProvider = "microsoft"

	// DefaultRecipientDomains are the envelope-recipient domains written into the Postfix
	// transport overlay when the breaker for DefaultProvider is OPEN. Postfix routes by
	// recipient domain, so the provider label is mapped to its concrete domains here.
	DefaultRecipientDomainsCSV = "outlook.com,hotmail.com,live.com,msn.com,hotmail.co.uk,live.co.uk,outlook.co.uk,passport.com"

	// DefaultStateFile is where the current failover domain set is written for the host hook.
	DefaultStateFile = "/var/lib/mxsentinel/failover-domains"
)

// Config is relayfailoverd's runtime configuration, resolved from the environment.
type Config struct {
	Enabled          bool          // MXS_FAILOVER_ENABLED (default false — opt-in)
	Provider         string        // MXS_FAILOVER_PROVIDER (default "microsoft")
	RecipientDomains []string      // MXS_FAILOVER_DOMAINS (default DefaultRecipientDomainsCSV)
	StateFile        string        // MXS_FAILOVER_STATE_FILE
	Interval         time.Duration // MXS_FAILOVER_INTERVAL — evaluation tick (default 1m)
	Window           time.Duration // MXS_FAILOVER_WINDOW — defer-rate measurement window (default 10m)
	TenantID         string        // RELAY_TENANT_ID — tenant to attach incidents to ("" -> no incidents)

	Policy Policy
}

// LoadConfig reads relayfailoverd's configuration from the environment with safe defaults.
func LoadConfig() Config {
	c := Config{
		Enabled:          getBool("MXS_FAILOVER_ENABLED", false),
		Provider:         getenv("MXS_FAILOVER_PROVIDER", DefaultProvider),
		RecipientDomains: splitCSV(getenv("MXS_FAILOVER_DOMAINS", DefaultRecipientDomainsCSV)),
		StateFile:        getenv("MXS_FAILOVER_STATE_FILE", DefaultStateFile),
		Interval:         parseDuration("MXS_FAILOVER_INTERVAL", time.Minute),
		Window:           parseDuration("MXS_FAILOVER_WINDOW", 10*time.Minute),
		TenantID:         strings.TrimSpace(os.Getenv("RELAY_TENANT_ID")),
		Policy:           LoadPolicy(),
	}
	return c
}

func getenv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return v
	}
	return def
}

func getBool(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		return def
	}
	return b
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

func parseFloat(key string, def float64) float64 {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || f < 0 {
		return def
	}
	return f
}

func parseUint(key string, def uint64) uint64 {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
	if err != nil {
		return def
	}
	return n
}

func splitCSV(s string) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, p := range strings.Split(s, ",") {
		t := strings.ToLower(strings.TrimSpace(p))
		if t == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}
