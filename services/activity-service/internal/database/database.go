// Package database connects to PostgreSQL and applies the SQL files in
// migrations/ in filename order. Each file runs once; the schema_migrations
// table remembers which ones already ran.
package database

import (
	"context"
	"embed"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Pool sizing. Five services share one Postgres (default limit 100 connections), so each keeps
// to 10. Connections are recycled so a long-running pool never holds a stale one, and idle ones
// are given back after five minutes.
const (
	maxConns          = 10
	minConns          = 1
	maxConnLifetime   = 30 * time.Minute
	maxConnIdleTime   = 5 * time.Minute
	healthCheckPeriod = time.Minute

	// migrationLockID is the Postgres advisory lock that makes starting instances take turns
	// migrating. Any fixed number will do: the lock lives inside this service's own database.
	migrationLockID = 727274
)

// poolConfig reads the connection settings from url and applies this project's pool sizing,
// unless the URL already chose its own (pool_max_conns and the like).
func poolConfig(url string) (*pgxpool.Config, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(url, "pool_max_conns") {
		cfg.MaxConns, cfg.MinConns = maxConns, minConns
	}
	cfg.MaxConnLifetime, cfg.MaxConnIdleTime, cfg.HealthCheckPeriod = maxConnLifetime, maxConnIdleTime, healthCheckPeriod
	return cfg, nil
}

// Connect opens a connection pool and waits for the database to answer.
// In Docker the database can take a few seconds to start, so we retry.
func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := poolConfig(url)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	var pingErr error
	for attempt := 1; attempt <= 20; attempt++ {
		if pingErr = pool.Ping(ctx); pingErr == nil {
			return pool, nil
		}
		log.Printf("database not ready (attempt %d/20): %v", attempt, pingErr)
		time.Sleep(time.Second)
	}
	pool.Close()
	return nil, fmt.Errorf("database unreachable: %w", pingErr)
}

// Migrate applies every migration that has not run yet. A Postgres advisory lock makes several
// instances starting at the same moment take turns, so two of them never run the same file.
// Migrations only go forward: a mistake is fixed with a new migration, not by undoing one.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	lock, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer lock.Release()
	if _, err := lock.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockID); err != nil {
		return err
	}
	// Unlock with a fresh context: ctx may be cancelled by now, and the lock must not outlive us.
	defer lock.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, migrationLockID)

	_, err = pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    text PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`)
	if err != nil {
		return err
	}

	files, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return err
	}
	for _, f := range files { // ReadDir returns files sorted by name
		var done bool
		err := pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, f.Name()).Scan(&done)
		if err != nil {
			return err
		}
		if done {
			continue
		}
		body, err := migrationFiles.ReadFile("migrations/" + f.Name())
		if err != nil {
			return err
		}
		if err := apply(ctx, pool, f.Name(), string(body)); err != nil {
			return fmt.Errorf("migration %s: %w", f.Name(), err)
		}
		log.Printf("applied migration %s", f.Name())
	}
	return nil
}

func apply(ctx context.Context, pool *pgxpool.Pool, name, body string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) // does nothing after a successful Commit

	if _, err := tx.Exec(ctx, body); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, name); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
