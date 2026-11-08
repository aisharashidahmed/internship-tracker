// Package db opens the Postgres connection pool and applies schema migrations.
//
// Migrations are plain .sql files in ./migrations, compiled into the binary
// with go:embed so the program never depends on the current directory.
package db

import (
	"context"
	"embed"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrations embed.FS

// DefaultURL is the local development database.
const DefaultURL = "postgres://localhost:5432/internship_tracker?sslmode=disable"

// URLFromEnv returns $DATABASE_URL, or DefaultURL if it isn't set.
func URLFromEnv() string {
	if url := os.Getenv("DATABASE_URL"); url != "" {
		return url
	}
	return DefaultURL
}

// Connect opens a connection pool and checks that the database answers.
func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("reach database: %w", err)
	}
	return pool, nil
}

// Migrate brings the schema up to the newest migration. Already-applied
// migrations are skipped, so it is safe to run every time.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	goose.SetBaseFS(migrations)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	sqlDB := stdlib.OpenDBFromPool(pool) // goose speaks database/sql
	defer sqlDB.Close()
	if err := goose.UpContext(ctx, sqlDB, "migrations"); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}
