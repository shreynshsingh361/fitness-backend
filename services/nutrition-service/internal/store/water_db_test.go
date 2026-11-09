package store_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"fitness/nutrition-service/internal/store"
)

// Water requests are retried by the offline queue and by lost replies, so a repeat must change
// nothing. Needs a throwaway Postgres (see testPool).
func TestWaterRequestsSentTwiceAreAppliedOnce(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	s := store.New(pool)
	user := uuid.NewString()
	day := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	total := func() int {
		n, err := s.WaterTotal(ctx, user, day)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	check := func(what string, want int) {
		t.Helper()
		if got := total(); got != want {
			t.Fatalf("%s: total = %d, want %d", what, got, want)
		}
	}

	op1, op2, op3 := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if err := s.AddWater(ctx, user, day, 250, op1); err != nil {
		t.Fatal(err)
	}
	check("first add", 250)
	if err := s.AddWater(ctx, user, day, 250, op1); err != nil {
		t.Fatal(err)
	}
	check("the same add sent again (a lost reply)", 250)
	if err := s.AddWater(ctx, user, day, 250, op2); err != nil {
		t.Fatal(err)
	}
	check("a different add", 500)

	if err := s.RemoveWater(ctx, user, day, 250, op3); err != nil {
		t.Fatal(err)
	}
	check("a removal", 250)
	if err := s.RemoveWater(ctx, user, day, 250, op3); err != nil {
		t.Fatal(err)
	}
	check("the same removal sent again", 250)

	// An older app sends no id: every request counts, as before.
	for range 2 {
		if err := s.AddWater(ctx, user, day, 100, ""); err != nil {
			t.Fatal(err)
		}
	}
	check("two adds with no id", 450)

	// Someone else using the same id is a different person's request.
	other := uuid.NewString()
	if err := s.AddWater(ctx, other, day, 250, op1); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.WaterTotal(ctx, other, day); n != 250 {
		t.Fatalf("another user with the same op id: total = %d, want 250", n)
	}

	// The same request arriving at the same moment twice.
	race := uuid.NewString()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.AddWater(ctx, user, day, 50, race)
		}()
	}
	wg.Wait()
	check("eight copies of one request at once", 500)

	// Deleting the account takes the op records with it.
	if err := s.DeleteUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM water_ops WHERE user_id = $1`, user).Scan(&left); err != nil || left != 0 {
		t.Fatalf("water_ops left after deleting the user: %d (%v)", left, err)
	}
}
