package store_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	"fitness/social-service/internal/database"
	"fitness/social-service/internal/store"
)

// Break #3: the rush. A group's invite code goes round a WhatsApp group and thirty people tap
// Join in the same second while the group has room for exactly one more. The limit is 100 people;
// the group must never end up with 101, and everyone who did not get in must be told it is full.
// Needs a throwaway Postgres: set SOCIAL_TEST_DATABASE_URL (never a database with real data).
func TestBreak3JoinRushNeverPassesTheGroupLimit(t *testing.T) {
	url := os.Getenv("SOCIAL_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("SOCIAL_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := database.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	s := store.New(pool)

	group, err := s.CreateGroup(ctx, uuid.NewString(), "Office steps", "")
	if err != nil {
		t.Fatal(err)
	}
	// One member so far (the creator). Fill the group up to one place short of full.
	if _, err := pool.Exec(ctx,
		`INSERT INTO group_members (group_id, user_id)
		 SELECT $1, gen_random_uuid() FROM generate_series(1, $2)`, group.ID, store.MaxGroupMembers-2); err != nil {
		t.Fatal(err)
	}

	const rush = 30
	var joined, full atomic.Int32
	var wg sync.WaitGroup
	for range rush {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, isNew, err := s.JoinGroup(ctx, group.InviteCode, uuid.NewString())
			switch {
			case errors.Is(err, store.ErrGroupFull):
				full.Add(1)
			case err != nil:
				t.Errorf("JoinGroup: %v", err)
			case isNew:
				joined.Add(1)
			}
		}()
	}
	wg.Wait()

	var members int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM group_members WHERE group_id = $1`, group.ID).Scan(&members); err != nil {
		t.Fatal(err)
	}
	if members != store.MaxGroupMembers {
		t.Fatalf("the group has %d members after the rush, want exactly %d", members, store.MaxGroupMembers)
	}
	if joined.Load() != 1 || full.Load() != rush-1 {
		t.Fatalf("joined = %d, told full = %d; want 1 and %d", joined.Load(), full.Load(), rush-1)
	}
}
