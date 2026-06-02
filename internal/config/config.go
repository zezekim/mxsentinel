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
