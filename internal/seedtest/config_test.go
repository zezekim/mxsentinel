package seedtest

import (
	"testing"
	"time"
)

func TestLoadConfigDefaults(t *testing.T) {
	// No MXS_SEEDTEST_* env set (t.Setenv not used) -> defaults.
	t.Setenv("MXS_SEEDTEST_SMTP_HOST", "")
	c := LoadConfig()
	if c.Interval != DefaultInterval {
		t.Errorf("interval = %v, want %v", c.Interval, DefaultInterval)
	}
	if c.CollectWindow != DefaultCollectWindow {
		t.Errorf("collect window = %v, want %v", c.CollectWindow, DefaultCollectWindow)
	}
	if c.SMTP.Configured() {
		t.Error("SMTP should not be configured without a host")
	}
	if c.SMTP.Port != DefaultSMTPPort {
		t.Errorf("smtp port = %d, want %d", c.SMTP.Port, DefaultSMTPPort)
	}
	if !c.SMTP.StartTLS {
		t.Error("StartTLS should default to true")
	}
	if len(c.IMAPAccounts) != 0 {
		t.Errorf("expected no imap accounts, got %d", len(c.IMAPAccounts))
	}
}

func TestLoadConfigFromEnv(t *testing.T) {
	t.Setenv("MXS_SEEDTEST_SMTP_HOST", "relay.example.com")
	t.Setenv("MXS_SEEDTEST_SMTP_PORT", "2525")
	t.Setenv("MXS_SEEDTEST_SMTP_USERNAME", "probe@relay")
	t.Setenv("MXS_SEEDTEST_SMTP_FROM", "probe@relay.example.com")
	t.Setenv("MXS_SEEDTEST_SMTP_STARTTLS", "false")
	t.Setenv("MXS_SEEDTEST_INTERVAL", "45s")
	t.Setenv("MXS_SEEDTEST_COLLECT_WINDOW", "1h")
	t.Setenv("MXS_SEEDTEST_IMAP_ACCOUNTS", `[
		{"address":"Seed@Gmail.com","host":"imap.gmail.com","username":"seed@gmail.com","password":"p"},
		{"address":"seed@outlook.com","host":"outlook.office365.com","port":993,"password":"q","spam_mailbox":"Junk"}
	]`)

	c := LoadConfig()
	if !c.SMTP.Configured() || c.SMTP.Host != "relay.example.com" || c.SMTP.Port != 2525 {
		t.Errorf("smtp = %+v", c.SMTP)
	}
	if c.SMTP.StartTLS {
		t.Error("StartTLS should be false")
	}
	if c.Interval != 45*time.Second || c.CollectWindow != time.Hour {
		t.Errorf("durations = %v / %v", c.Interval, c.CollectWindow)
	}
	if len(c.IMAPAccounts) != 2 {
		t.Fatalf("imap accounts = %d, want 2", len(c.IMAPAccounts))
	}

	// lookup is case-insensitive on address
	g, ok := c.AccountFor("seed@gmail.com")
	if !ok {
		t.Fatal("gmail account not found (case-insensitive lookup)")
	}
	if g.Port != DefaultIMAPPort {
		t.Errorf("gmail port default = %d, want %d", g.Port, DefaultIMAPPort)
	}
	if g.Username != "seed@gmail.com" {
		t.Errorf("gmail username = %q", g.Username)
	}

	o, ok := c.AccountFor("seed@outlook.com")
	if !ok {
		t.Fatal("outlook account not found")
	}
	if o.SpamMailbox != "Junk" {
		t.Errorf("outlook spam mailbox = %q", o.SpamMailbox)
	}
	// username defaults to address when omitted
	if o.Username != "seed@outlook.com" {
		t.Errorf("outlook username default = %q", o.Username)
	}
}

func TestLoadConfigInvalidIMAPJSON(t *testing.T) {
	t.Setenv("MXS_SEEDTEST_IMAP_ACCOUNTS", "{not json")
	c := LoadConfig()
	if len(c.IMAPAccounts) != 0 {
		t.Errorf("invalid JSON should yield no accounts, got %d", len(c.IMAPAccounts))
	}
}
