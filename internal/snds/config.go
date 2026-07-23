package snds

import (
	"os"
	"strings"
	"time"
)

// Default endpoints / knobs. Overridable via MXS_* env (see LoadConfig).
const (
	// DefaultDataURL is Microsoft's SNDS automated-data endpoint. The access key is passed
	// as the `key` query parameter; the response is header-less CSV, one row per IP per
	// activity period. See docs/microsoft-snds-jmrp.md.
	DefaultDataURL = "https://postmaster.live.com/snds/data.aspx"

	// DefaultJMRPDir is the drop directory watched for JMRP ARF complaint emails when
	// MXS_JMRP_DIR is unset.
	DefaultJMRPDir = "/jmrp-drop"

	// DefaultInterval is the SNDS poll interval when MXS_SNDS_INTERVAL is unset/invalid.
	DefaultInterval = 6 * time.Hour

	// DefaultComplaintThreshold is the 24h JMRP complaint count (per sending domain) that
	// trips a critical incident when MXS_JMRP_COMPLAINT_THRESHOLD is unset/invalid.
	DefaultComplaintThreshold = 10

	// DefaultScanInterval is how often the JMRP drop directory is scanned.
	DefaultScanInterval = 30 * time.Second
)

// Config holds sndsd's runtime configuration, resolved from environment variables with safe
// defaults. It mirrors internal/rbl.Config's style. It never fabricates credentials: with
// no SNDS access key the SNDS poll half is skipped cleanly (JMRP ingest still runs), exactly
// as cmd/fbld skips the Postmaster half when GOOGLE_POSTMASTER_TOKEN is unset.
type Config struct {
	AccessKey          string        // MXS_SNDS_KEY (empty -> SNDS poll disabled)
	DataURL            string        // MXS_SNDS_URL
	JMRPDir            string        // MXS_JMRP_DIR
	Interval           time.Duration // MXS_SNDS_INTERVAL (SNDS CSV poll)
	ScanInterval       time.Duration // MXS_JMRP_SCAN_INTERVAL (drop-dir scan)
	ComplaintThreshold int           // MXS_JMRP_COMPLAINT_THRESHOLD
}

// LoadConfig reads sndsd's configuration from the environment.
func LoadConfig() Config {
	return Config{
		AccessKey:          strings.TrimSpace(os.Getenv("MXS_SNDS_KEY")),
		DataURL:            getenv("MXS_SNDS_URL", DefaultDataURL),
		JMRPDir:            getenv("MXS_JMRP_DIR", DefaultJMRPDir),
		Interval:           parseDuration("MXS_SNDS_INTERVAL", DefaultInterval),
		ScanInterval:       parseDuration("MXS_JMRP_SCAN_INTERVAL", DefaultScanInterval),
		ComplaintThreshold: parseInt("MXS_JMRP_COMPLAINT_THRESHOLD", DefaultComplaintThreshold),
	}
}

// SNDSEnabled reports whether an SNDS access key is configured.
func (c Config) SNDSEnabled() bool { return c.AccessKey != "" }

func getenv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
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

func parseInt(key string, def int) int {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	n := 0
	trimmed := strings.TrimSpace(v)
	if trimmed == "" {
		return def
	}
	for _, r := range trimmed {
		if r < '0' || r > '9' {
			return def
		}
		n = n*10 + int(r-'0')
	}
	if n <= 0 {
		return def
	}
	return n
}
