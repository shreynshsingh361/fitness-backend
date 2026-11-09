package store_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"fitness/nutrition-service/internal/store"
)

// Break #1: the double tap. A user on a bad connection taps Add food and the app, the offline
// queue and the user all send the same request again and again, some at the same moment.
// Whatever the count, the diary must end up with exactly one entry, and exactly one of the
// requests may be told "created".
func TestBreak1DoubleTapStormSavesOneEntry(t *testing.T) {
	pool := testPool(t)
	s := store.New(pool)
	ctx := context.Background()
	user, op := uuid.NewString(), uuid.NewString()
	var food string
	if err := pool.QueryRow(ctx, `SELECT id FROM foods WHERE owner_id IS NULL LIMIT 1`).Scan(&food); err != nil {
		t.Fatal(err)
	}
	entry := store.NewEntry{ClientOpID: op, FoodID: food, Meal: "breakfast", Servings: 1, EatenOn: time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)}

	var created atomic.Int32
	var wg sync.WaitGroup
	for range 25 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, fresh, err := s.CreateEntry(ctx, user, entry); err != nil {
				t.Errorf("CreateEntry: %v", err)
			} else if fresh {
				created.Add(1)
			}
		}()
	}
	wg.Wait()

	if got := created.Load(); got != 1 {
		t.Fatalf("%d of 25 identical requests were told they created the entry, want exactly 1", got)
	}
	entries, _, err := s.DiaryForDay(ctx, user, entry.EatenOn)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("the diary has %d entries after 25 identical requests, want 1", len(entries))
	}
}
