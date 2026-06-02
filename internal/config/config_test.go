package config

import "testing"

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Service != "mxsentinel" {
		t.Errorf("service = %q, want mxsentinel", cfg.Service)
	}
	if cfg.Redis.Addr != "localhost:6379" {
		t.Errorf("redis addr = %q", cfg.Redis.Addr)
	}
	if cfg.Postgres.MaxConns != 10 {
		t.Errorf("postgres maxconns = %d, want 10", cfg.Postgres.MaxConns)
	}
}

func TestEnvOverride(t *testing.T) {
	t.Setenv("MXS_NATS_URL", "nats://example:4222")
	t.Setenv("MXS_POSTGRES_MAXCONNS", "42")
	t.Setenv("MXS_OBJECTSTORE_USESSL", "true")
	t.Setenv("MXS_LOGLEVEL", "debug")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.NATS.URL != "nats://example:4222" {
		t.Errorf("nats url = %q", cfg.NATS.URL)
	}
	if cfg.Postgres.MaxConns != 42 {
		t.Errorf("maxconns = %d, want 42", cfg.Postgres.MaxConns)
	}
	if !cfg.ObjectStore.UseSSL {
		t.Errorf("usessl = false, want true")
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("loglevel = %q, want debug", cfg.LogLevel)
	}
	// Untouched values keep their defaults.
	if cfg.Redis.Addr != "localhost:6379" {
		t.Errorf("redis addr default lost: %q", cfg.Redis.Addr)
	}
}

func TestEnvBadInt(t *testing.T) {
	t.Setenv("MXS_POSTGRES_MAXCONNS", "not-a-number")
	if _, err := Load(""); err == nil {
		t.Fatal("expected error for non-integer MXS_POSTGRES_MAXCONNS")
	}
}
