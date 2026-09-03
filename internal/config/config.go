// Package config loads MX Sentinel service configuration from an optional YAML file
// overlaid with environment variables (12-factor). Env vars take precedence.
//
// Env mapping: MXS_<PATH> where path segments are joined by "_" and each segment is a
// single word (no internal underscores), e.g.
//
//	MXS_POSTGRES_DSN          -> postgres.dsn
//	MXS_CLICKHOUSE_ADDR       -> clickhouse.addr
//	MXS_OBJECTSTORE_ACCESSKEY -> objectstore.accesskey
package config

import (
	"fmt"
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

// Config is the full configuration surface shared by all services. A given service only
// uses the sections it needs.
type Config struct {
	Service     string            `yaml:"service"`
	LogLevel    string            `yaml:"loglevel"`
	HTTPAddr    string            `yaml:"httpaddr"` // metrics + health endpoint
	Postgres    PostgresConfig    `yaml:"postgres"`
	ClickHouse  ClickHouseConfig  `yaml:"clickhouse"`
	Redis       RedisConfig       `yaml:"redis"`
	NATS        NATSConfig        `yaml:"nats"`
	ObjectStore ObjectStoreConfig `yaml:"objectstore"`
	AI          AIConfig          `yaml:"ai"`
	Integration IntegrationConfig `yaml:"integration"`
	DmarcPull   DmarcPullConfig   `yaml:"dmarcp"`
	Webmail     WebmailConfig     `yaml:"webmail"`
}

// WebmailConfig wires the Roundcube autologin handoff (docs/webmail-autologin.md). apid
// mints a single-use token for an SMTP user; the Roundcube mxs_autologin plugin redeems it
// and is told which IMAP endpoint to log the user into. The feature is off unless BaseURL
// and PluginSecret are both set.
type WebmailConfig struct {
	// BaseURL is the public Roundcube origin+path, e.g. https://sentinel.example.com/roundcube.
	BaseURL string `yaml:"baseurl"`
	// PluginSecret authenticates the Roundcube plugin to POST /v1/webmail/redeem. That
	// endpoint sits outside the tenant auth pipeline (the plugin holds no API token), so
	// this secret is the only thing standing between a leaked token and a redemption.
	// Generate with: openssl rand -hex 32
	PluginSecret string `yaml:"pluginsecret"`
	// IMAPHost is the hostname Roundcube connects to for IMAP — resolved from ROUNDCUBE's
	// network, not apid's. With Roundcube in Docker and Dovecot on the host that is usually
	// host.docker.internal or the docker bridge gateway.
	IMAPHost string `yaml:"imaphost"`
	IMAPPort int    `yaml:"imapport"`
	// IMAPTLS selects how Roundcube reaches IMAP: "starttls" (tls://, port 143),
	// "tls" (ssl://, port 993), or "none" (plaintext — only sane over a private network).
	IMAPTLS string `yaml:"imaptls"`
	// TokenTTLSecs is how long a minted autologin token stays redeemable. Seconds, not
	// minutes: the token travels from the dashboard to Roundcube in one redirect.
	TokenTTLSecs int `yaml:"tokenttlsecs"`
}

// DmarcPullConfig configures the external DMARC receiver pull integration.
type DmarcPullConfig struct {
	BaseURL      string `yaml:"baseurl"` // e.g. https://dmarc.example.com/api/v1
	APIKey       string `yaml:"apikey"`
	TenantID     string `yaml:"tenantid"`     // MX Sentinel tenant to attribute pulled reports to
	Interval     int    `yaml:"interval"`     // seconds between polls; default 3600
	LookbackDays int    `yaml:"lookbackdays"` // on first run, how far back to fetch; default 30
}

// IntegrationConfig holds settings for external integrations (cPanel/WHMCS).
type IntegrationConfig struct {
	// EncryptionKey is a 64-char hex string (32 bytes) used for AES-256-GCM encryption
	// of stored credentials (cPanel API tokens, WHMCS secrets).
	// Generate with: openssl rand -hex 32
	// If empty, credentials are stored as plaintext with a startup warning.
	EncryptionKey string `yaml:"encryptionkey"`
}

// AIConfig points the AI reasoning layer at a local OpenAI-compatible LLM endpoint
// (Ollama, vLLM, llama.cpp). Metadata only — never message bodies.
type AIConfig struct {
	Endpoint    string `yaml:"endpoint"` // base URL, e.g. http://localhost:11434/v1
	Model       string `yaml:"model"`
	APIKey      string `yaml:"apikey"` // optional; many local servers ignore it
	TimeoutSecs int    `yaml:"timeoutsecs"`
}

type PostgresConfig struct {
	DSN      string `yaml:"dsn"`
	MaxConns int    `yaml:"maxconns"`
}

type ClickHouseConfig struct {
	Addr     string `yaml:"addr"`
	Database string `yaml:"database"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

type RedisConfig struct {
	Addr     string `yaml:"addr"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
}

type NATSConfig struct {
	URL string `yaml:"url"`
}

type ObjectStoreConfig struct {
	Endpoint  string `yaml:"endpoint"`
	Region    string `yaml:"region"`
	Bucket    string `yaml:"bucket"`
	AccessKey string `yaml:"accesskey"`
	SecretKey string `yaml:"secretkey"`
	UseSSL    bool   `yaml:"usessl"`
}

// Defaults returns config pre-populated with local-dev defaults matching
// deploy/docker-compose.yml. File and env values override these.
func Defaults() Config {
	return Config{
		Service:  "mxsentinel",
		LogLevel: "info",
		HTTPAddr: ":9090",
		Postgres: PostgresConfig{
			DSN:      "postgres://mxsentinel:mxsentinel@localhost:5432/mxsentinel?sslmode=disable",
			MaxConns: 10,
		},
		ClickHouse: ClickHouseConfig{
			Addr:     "localhost:9000",
			Database: "mxsentinel",
			Username: "default",
			Password: "",
		},
		Redis: RedisConfig{Addr: "localhost:6379"},
		NATS:  NATSConfig{URL: "nats://localhost:4222"},
		ObjectStore: ObjectStoreConfig{
			Endpoint:  "localhost:9001",
			Region:    "us-east-1",
			Bucket:    "mxsentinel",
			AccessKey: "minioadmin",
			SecretKey: "minioadmin",
			UseSSL:    false,
		},
		AI: AIConfig{
			Endpoint:    "http://localhost:11434/v1", // Ollama default
			Model:       "llama3",
			TimeoutSecs: 60,
		},
		Webmail: WebmailConfig{
			IMAPHost:     "host.docker.internal",
			IMAPPort:     143,
			IMAPTLS:      "starttls",
			TokenTTLSecs: 60,
		},
	}
}

// Load builds a Config from defaults, an optional YAML file at path (ignored if empty or
// missing), and MXS_-prefixed environment variables, in increasing precedence.
func Load(path string) (Config, error) {
	cfg := Defaults()

	if path != "" {
		b, err := os.ReadFile(path)
		switch {
		case err == nil:
			if err := yaml.Unmarshal(b, &cfg); err != nil {
				return cfg, fmt.Errorf("parse config file %q: %w", path, err)
			}
		case os.IsNotExist(err):
			// A missing explicit path is not fatal — env + defaults still apply.
		default:
			return cfg, fmt.Errorf("read config file %q: %w", path, err)
		}
	}

	if err := applyEnv(&cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func applyEnv(c *Config) error {
	setStr(&c.Service, "MXS_SERVICE")
	setStr(&c.LogLevel, "MXS_LOGLEVEL")
	setStr(&c.HTTPAddr, "MXS_HTTPADDR")

	setStr(&c.Postgres.DSN, "MXS_POSTGRES_DSN")
	if err := setInt(&c.Postgres.MaxConns, "MXS_POSTGRES_MAXCONNS"); err != nil {
		return err
	}

	setStr(&c.ClickHouse.Addr, "MXS_CLICKHOUSE_ADDR")
	setStr(&c.ClickHouse.Database, "MXS_CLICKHOUSE_DATABASE")
	setStr(&c.ClickHouse.Username, "MXS_CLICKHOUSE_USERNAME")
	setStr(&c.ClickHouse.Password, "MXS_CLICKHOUSE_PASSWORD")

	setStr(&c.Redis.Addr, "MXS_REDIS_ADDR")
	setStr(&c.Redis.Password, "MXS_REDIS_PASSWORD")
	if err := setInt(&c.Redis.DB, "MXS_REDIS_DB"); err != nil {
		return err
	}

	setStr(&c.NATS.URL, "MXS_NATS_URL")

	setStr(&c.ObjectStore.Endpoint, "MXS_OBJECTSTORE_ENDPOINT")
	setStr(&c.ObjectStore.Region, "MXS_OBJECTSTORE_REGION")
	setStr(&c.ObjectStore.Bucket, "MXS_OBJECTSTORE_BUCKET")
	setStr(&c.ObjectStore.AccessKey, "MXS_OBJECTSTORE_ACCESSKEY")
	setStr(&c.ObjectStore.SecretKey, "MXS_OBJECTSTORE_SECRETKEY")
	if err := setBool(&c.ObjectStore.UseSSL, "MXS_OBJECTSTORE_USESSL"); err != nil {
		return err
	}

	setStr(&c.AI.Endpoint, "MXS_AI_ENDPOINT")
	setStr(&c.AI.Model, "MXS_AI_MODEL")
	setStr(&c.AI.APIKey, "MXS_AI_APIKEY")
	if err := setInt(&c.AI.TimeoutSecs, "MXS_AI_TIMEOUTSECS"); err != nil {
		return err
	}

	setStr(&c.Integration.EncryptionKey, "MXS_ENCRYPTION_KEY")

	setStr(&c.Webmail.BaseURL, "MXS_WEBMAIL_BASEURL")
	setStr(&c.Webmail.PluginSecret, "MXS_WEBMAIL_PLUGINSECRET")
	setStr(&c.Webmail.IMAPHost, "MXS_WEBMAIL_IMAPHOST")
	setStr(&c.Webmail.IMAPTLS, "MXS_WEBMAIL_IMAPTLS")
	if err := setInt(&c.Webmail.IMAPPort, "MXS_WEBMAIL_IMAPPORT"); err != nil {
		return err
	}
	if err := setInt(&c.Webmail.TokenTTLSecs, "MXS_WEBMAIL_TOKENTTL"); err != nil {
		return err
	}

	setStr(&c.DmarcPull.BaseURL, "MXS_DMARCP_BASEURL")
	setStr(&c.DmarcPull.APIKey, "MXS_DMARCP_APIKEY")
	setStr(&c.DmarcPull.TenantID, "MXS_DMARCP_TENANTID")
	if err := setInt(&c.DmarcPull.Interval, "MXS_DMARCP_INTERVAL"); err != nil {
		return err
	}
	if err := setInt(&c.DmarcPull.LookbackDays, "MXS_DMARCP_LOOKBACKDAYS"); err != nil {
		return err
	}
	return nil
}

func setStr(dst *string, env string) {
	if v, ok := os.LookupEnv(env); ok {
		*dst = v
	}
}

func setInt(dst *int, env string) error {
	v, ok := os.LookupEnv(env)
	if !ok {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fmt.Errorf("%s: want integer, got %q: %w", env, v, err)
	}
	*dst = n
	return nil
}

func setBool(dst *bool, env string) error {
	v, ok := os.LookupEnv(env)
	if !ok {
		return nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fmt.Errorf("%s: want bool, got %q: %w", env, v, err)
	}
	*dst = b
	return nil
}
