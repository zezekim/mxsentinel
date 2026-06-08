package anomaly

import (
	"os"
	"strconv"
	"time"
)

// Config tunes the detector. Defaults are conservative for a shared relay; override via
// environment so an operator can tighten/loosen without a rebuild.
type Config struct {
	// Factor: current-hour count must exceed baseline*Factor to (relatively) trip.
	Factor float64
	// MinAbsolute: a floor so a tiny baseline can't trip on a handful of messages —
	// the effective threshold is max(baseline*Factor, MinAbsolute).
	MinAbsolute int64
	// MinSamples: completed hours that must be folded into a domain's EWMA before any
	// trip is allowed (warm-up). Below this the domain is "learning", never alerting.
	MinSamples int64
	// EWMAAlpha: smoothing factor for the EWMA update (0<alpha<=1). Higher = more
	// responsive to recent hours, lower = smoother/longer memory.
	EWMAAlpha float64
	// Cooldown: minimum time between detections (and incidents) for the same domain.
	Cooldown time.Duration
}

// LoadConfig reads the detector knobs from the environment, applying safe defaults.
//
//	ANOMALY_FACTOR        (float, default 5.0)
//	ANOMALY_MIN_ABS       (int,   default 100)
//	ANOMALY_MIN_SAMPLES   (int,   default 6)
//	ANOMALY_EWMA_ALPHA    (float, default 0.3)
//	ANOMALY_COOLDOWN      (Go duration, default 6h)
func LoadConfig() Config {
	c := Config{
		Factor:      5.0,
		MinAbsolute: 100,
		MinSamples:  6,
		EWMAAlpha:   0.3,
		Cooldown:    6 * time.Hour,
	}
	if v := os.Getenv("ANOMALY_FACTOR"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			c.Factor = f
		}
	}
	if v := os.Getenv("ANOMALY_MIN_ABS"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			c.MinAbsolute = n
		}
	}
	if v := os.Getenv("ANOMALY_MIN_SAMPLES"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			c.MinSamples = n
		}
	}
	if v := os.Getenv("ANOMALY_EWMA_ALPHA"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 && f <= 1 {
			c.EWMAAlpha = f
		}
	}
	if v := os.Getenv("ANOMALY_COOLDOWN"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			c.Cooldown = d
		}
	}
	return c
}
