package bounce

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Default runtime knobs for the bounced daemon. These are overridable via MXS_BOUNCE_* env
// vars (see LoadConfig) and, once the orchestrator wires them in, the central config.
const (
	// DefaultInterval is how often the daemon re-scans recent bounces.
	DefaultInterval = 5 * time.Minute
	// DefaultLookback is the window of recent bounces re-read each tick. It must comfortably
	// exceed the interval so nothing is missed between ticks; the day-level rollup is
	// recomputed authoritatively over this window each pass (idempotent).
	DefaultLookback = 48 * time.Hour
	// DefaultMaxRows caps how many recent bounce rows are pulled per tick (safety valve for
	// very high-volume relays). Rollup/suppression reflect up to this many most-recent rows.
	DefaultMaxRows = 100000
)

// Config holds the bounced daemon's runtime configuration, resolved from environment
// variables with safe defaults.
type Config struct {
	Interval time.Duration // MXS_BOUNCE_INTERVAL
	Lookback time.Duration // MXS_BOUNCE_LOOKBACK
	MaxRows  int           // MXS_BOUNCE_MAXROWS
}

// LoadConfig reads the bounced daemon configuration from the environment.
func LoadConfig() Config {
	return Config{
		Interval: parseDuration("MXS_BOUNCE_INTERVAL", DefaultInterval),
		Lookback: parseDuration("MXS_BOUNCE_LOOKBACK", DefaultLookback),
		MaxRows:  parseInt("MXS_BOUNCE_MAXROWS", DefaultMaxRows),
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

func parseInt(key string, def int) int {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return def
	}
	return n
}
