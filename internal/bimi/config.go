package bimi

import (
	"os"
	"strings"
	"time"
)

// DefaultInterval is bimid's poll cadence when MXS_BIMI_INTERVAL is unset/invalid. BIMI state
// changes slowly (DNS + certificate lifecycle), so an hourly sweep is ample.
const DefaultInterval = time.Hour

// DefaultFetchTimeout bounds each logo/VMC HTTP GET.
const DefaultFetchTimeout = 10 * time.Second

// Config holds bimid's runtime configuration, resolved from environment variables. It mirrors
// the pattern used by internal/rbl.LoadConfig so bimid stays independent of the central config.
type Config struct {
	Interval     time.Duration // MXS_BIMI_INTERVAL
	FetchTimeout time.Duration // MXS_BIMI_FETCH_TIMEOUT
}

// LoadConfig reads bimid's configuration from the environment with safe defaults.
func LoadConfig() Config {
	return Config{
		Interval:     parseDuration("MXS_BIMI_INTERVAL", DefaultInterval),
		FetchTimeout: parseDuration("MXS_BIMI_FETCH_TIMEOUT", DefaultFetchTimeout),
	}
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
