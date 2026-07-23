package seedtest

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"time"
)

// Default tuning constants for seedd.
const (
	// DefaultInterval is how often seedd wakes to advance runs (send probes, poll collectors).
	DefaultInterval = 2 * time.Minute
	// DefaultCollectWindow is how long a "sent" probe is polled for before it is declared
	// missing and the result finalized.
	DefaultCollectWindow = 30 * time.Minute
	// DefaultSMTPPort is the submission port used when none is configured.
	DefaultSMTPPort = 587
	// DefaultIMAPPort is the IMAPS port used when a seed account omits one.
	DefaultIMAPPort = 993
)

// SMTPConfig points the probe sender at the relay's submission endpoint. Probes go through the
// same relay as production mail so their placement reflects the real sending IP/reputation.
type SMTPConfig struct {
	Host     string // MXS_SEEDTEST_SMTP_HOST
	Port     int    // MXS_SEEDTEST_SMTP_PORT (default 587)
	Username string // MXS_SEEDTEST_SMTP_USERNAME (optional; empty => no AUTH)
	Password string // MXS_SEEDTEST_SMTP_PASSWORD
	From     string // MXS_SEEDTEST_SMTP_FROM (default envelope/header From for probes)
	StartTLS bool   // MXS_SEEDTEST_SMTP_STARTTLS (default true)
}

// Configured reports whether enough is set to actually send probes.
func (c SMTPConfig) Configured() bool { return strings.TrimSpace(c.Host) != "" }

// IMAPAccount holds the credentials for polling a single seed mailbox. These live in seedd's
// environment (never in the database) so seed-mailbox passwords are not persisted per the
// privacy/security boundary.
type IMAPAccount struct {
	Address     string `json:"address"`      // the seed email this account reads
	Host        string `json:"host"`         // IMAP server host
	Port        int    `json:"port"`         // IMAP server port (default 993, implicit TLS)
	Username    string `json:"username"`     // login (defaults to Address when empty)
	Password    string `json:"password"`     // login secret
	SpamMailbox string `json:"spam_mailbox"` // junk folder name (default provider-specific, see SpamFolders)
}

// SpamFolders lists candidate junk-folder names to search per provider when an account does
// not specify SpamMailbox explicitly.
var SpamFolders = map[string][]string{
	ProviderGmail:   {"[Gmail]/Spam", "Spam"},
	ProviderOutlook: {"Junk", "Junk Email"},
	ProviderYahoo:   {"Bulk Mail", "Bulk", "Spam"},
	ProviderOther:   {"Spam", "Junk"},
}

// Config is seedd's fully-resolved runtime configuration.
type Config struct {
	SMTP          SMTPConfig
	IMAPAccounts  map[string]IMAPAccount // keyed by lower-cased seed address
	Interval      time.Duration
	CollectWindow time.Duration
}

// LoadConfig reads seedd's configuration from MXS_SEEDTEST_* environment variables.
//
// Keys:
//
//	MXS_SEEDTEST_SMTP_HOST / _PORT / _USERNAME / _PASSWORD / _FROM / _STARTTLS
//	MXS_SEEDTEST_IMAP_ACCOUNTS   JSON array of IMAPAccount objects (see IMAPAccount tags)
//	MXS_SEEDTEST_INTERVAL        Go duration; how often to advance runs (default 2m)
//	MXS_SEEDTEST_COLLECT_WINDOW  Go duration; poll window before a probe is "missing" (default 30m)
//
// LoadConfig is pure over the environment and never dials anything, so it is safe to unit-test.
func LoadConfig() Config {
	c := Config{
		SMTP: SMTPConfig{
			Host:     getenv("MXS_SEEDTEST_SMTP_HOST", ""),
			Port:     getenvInt("MXS_SEEDTEST_SMTP_PORT", DefaultSMTPPort),
			Username: getenv("MXS_SEEDTEST_SMTP_USERNAME", ""),
			Password: getenv("MXS_SEEDTEST_SMTP_PASSWORD", ""),
			From:     getenv("MXS_SEEDTEST_SMTP_FROM", ""),
			StartTLS: getenvBool("MXS_SEEDTEST_SMTP_STARTTLS", true),
		},
		IMAPAccounts:  parseIMAPAccounts(os.Getenv("MXS_SEEDTEST_IMAP_ACCOUNTS")),
		Interval:      getenvDuration("MXS_SEEDTEST_INTERVAL", DefaultInterval),
		CollectWindow: getenvDuration("MXS_SEEDTEST_COLLECT_WINDOW", DefaultCollectWindow),
	}
	return c
}

// AccountFor returns the IMAP account configured for a seed address, if any.
func (c Config) AccountFor(address string) (IMAPAccount, bool) {
	a, ok := c.IMAPAccounts[strings.ToLower(strings.TrimSpace(address))]
	return a, ok
}

// parseIMAPAccounts decodes the JSON account array, filling per-account defaults. Invalid or
// empty input yields an empty (non-nil) map — seedd then simply leaves those results pending.
func parseIMAPAccounts(raw string) map[string]IMAPAccount {
	out := map[string]IMAPAccount{}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return out
	}
	var accts []IMAPAccount
	if err := json.Unmarshal([]byte(raw), &accts); err != nil {
		return out
	}
	for _, a := range accts {
		a.Address = strings.TrimSpace(a.Address)
		if a.Address == "" {
			continue
		}
		if a.Port == 0 {
			a.Port = DefaultIMAPPort
		}
		if strings.TrimSpace(a.Username) == "" {
			a.Username = a.Address
		}
		out[strings.ToLower(a.Address)] = a
	}
	return out
}

func getenv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return v
	}
	return def
}

func getenvInt(key string, def int) int {
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

func getenvBool(key string, def bool) bool {
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

func getenvDuration(key string, def time.Duration) time.Duration {
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
