// Package database connects to PostgreSQL and applies the SQL files in
// migrations/ in filename order. Each file runs once; the schema_migrations
// table remembers which ones already ran.
package database

import (
	"context"
	"embed"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Connect opens a connection pool and waits for the database to answer.
// In Docker the database can take a few seconds to start, so we retry.
func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
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

// Migrate applies every migration that has not run yet.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
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
