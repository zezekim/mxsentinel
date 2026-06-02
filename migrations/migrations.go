// Package migrations embeds and runs the versioned database migrations for both
// PostgreSQL and ClickHouse using goose. The migration SQL is derived from the reference
// DDL in /schemas (see docs/phase-1-plan.md WS1).
package migrations

import (
	"database/sql"
	"embed"
	"fmt"

	"github.com/ClickHouse/clickhouse-go/v2"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"github.com/pressly/goose/v3"

	"github.com/zezekim/mxsentinel/internal/config"
)

//go:embed postgres clickhouse
var migFS embed.FS

// Command is a migration action.
type Command string

const (
	Up      Command = "up"
	Down    Command = "down"
	Status  Command = "status"
	Version Command = "version"
)

// Postgres runs a migration command against PostgreSQL.
func Postgres(cfg config.PostgresConfig, cmd Command) error {
	db, err := sql.Open("pgx", cfg.DSN)
	if err != nil {
		return fmt.Errorf("open postgres: %w", err)
	}
	defer db.Close()
	return run(db, "postgres", "postgres", cmd)
}

// ClickHouse runs a migration command against ClickHouse.
func ClickHouse(cfg config.ClickHouseConfig, cmd Command) error {
	db := clickhouse.OpenDB(&clickhouse.Options{
		Addr: []string{cfg.Addr},
		Auth: clickhouse.Auth{
			Database: cfg.Database,
			Username: cfg.Username,
			Password: cfg.Password,
		},
	})
	defer db.Close()
	return run(db, "clickhouse", "clickhouse", cmd)
}

func run(db *sql.DB, dialect, dir string, cmd Command) error {
	goose.SetBaseFS(migFS)
	if err := goose.SetDialect(dialect); err != nil {
		return fmt.Errorf("set goose dialect %q: %w", dialect, err)
	}
	switch cmd {
	case Up:
		return goose.Up(db, dir)
	case Down:
		return goose.Down(db, dir)
	case Status:
		return goose.Status(db, dir)
	case Version:
		return goose.Version(db, dir)
	default:
		return fmt.Errorf("unknown migration command %q", cmd)
	}
}
