package database

import (
	"context"
	"os"
	"sync"
	"testing"
)

// The pool is sized for five services sharing one Postgres, unless the URL chooses its own size.
func TestPoolConfigAppliesTheProjectSizing(t *testing.T) {
	cfg, err := poolConfig("postgres://u:p@localhost/db")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxConns != maxConns || cfg.MinConns != minConns {
		t.Errorf("pool size = %d..%d, want %d..%d", cfg.MinConns, cfg.MaxConns, minConns, maxConns)
	}
	if cfg.MaxConnLifetime != maxConnLifetime || cfg.MaxConnIdleTime != maxConnIdleTime {
		t.Errorf("lifetime/idle = %v/%v, want %v/%v", cfg.MaxConnLifetime, cfg.MaxConnIdleTime, maxConnLifetime, maxConnIdleTime)
	}

	own, err := poolConfig("postgres://u:p@localhost/db?pool_max_conns=3")
	if err != nil {
		t.Fatal(err)
	}
	if own.MaxConns != 3 {
		t.Errorf("a size chosen in the URL must win: MaxConns = %d, want 3", own.MaxConns)
	}
	if _, err := poolConfig("not a url"); err == nil {
		t.Error("a bad URL must be an error")
	}
}

// Several instances starting at once (a rolling deploy, a restart loop) must not run the same
// migration twice or fail with a duplicate-table error. Needs a throwaway, empty Postgres:
// set NUTRITION_TEST_DATABASE_URL. The test wipes the public schema first, so never a real one.
func TestMigrateTakesTurnsWhenInstancesStartTogether(t *testing.T) {
	url := os.Getenv("NUTRITION_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("NUTRITION_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}

	const instances = 8
	errs := make([]error, instances)
	var wg sync.WaitGroup
	for i := range instances {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = Migrate(ctx, pool)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("instance %d: %v", i, err)
		}
	}

	files, _ := migrationFiles.ReadDir("migrations")
	var applied int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != len(files) {
		t.Errorf("%d migrations recorded, want %d (each exactly once)", applied, len(files))
	}
}
