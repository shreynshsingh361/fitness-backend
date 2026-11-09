package store_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"fitness/nutrition-service/internal/database"
)

// testPool connects to the throwaway Postgres named by NUTRITION_TEST_DATABASE_URL and applies
// the migrations. It skips the test when none is set. Never point it at a database with real data.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("NUTRITION_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("NUTRITION_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := database.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool
}
