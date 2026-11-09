package store_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"fitness/auth-service/internal/database"
	"fitness/auth-service/internal/store"
)

// A stolen refresh token: the thief and the real user each hold a copy. The first to use it wins
// the new token; when the other one then shows up with the old copy, every session of that user
// must end, because nobody can tell who is who. Needs a throwaway Postgres: AUTH_TEST_DATABASE_URL.
func TestRefreshTokenReuseEndsEverySession(t *testing.T) {
	url := os.Getenv("AUTH_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("AUTH_TEST_DATABASE_URL is not set")
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

	user := mustUser(t, s, "victim@example.com")
	other := mustUser(t, s, "bystander@example.com")
	exp := time.Now().Add(time.Hour)
	save := func(u store.User, hash string) {
		t.Helper()
		if err := s.SaveRefreshToken(ctx, u.ID, hash, exp); err != nil {
			t.Fatal(err)
		}
	}
	rotate := func(oldHash, newHash string) error {
		_, err := s.RotateRefreshToken(ctx, oldHash, newHash, exp)
		return err
	}

	save(user, "A")        // the token both the user and the thief have a copy of
	save(user, "phone-2")  // the same user's other device
	save(other, "other-1") // someone else entirely

	// The thief uses A first and gets B. A is now spent.
	if err := rotate("A", "B"); err != nil {
		t.Fatalf("first use of A: %v", err)
	}
	// The user's own retry with A a moment later (a lost reply) is only refused, not treated as theft.
	if err := rotate("A", "B2"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("A again straight away: err = %v, want ErrNotFound", err)
	}
	if err := rotate("B", "C"); err != nil {
		t.Fatalf("B must still work after a retry inside the grace period: %v", err)
	}

	// Later the real user's copy of A turns up: that is reuse, and ends all of the user's sessions.
	if _, err := pool.Exec(ctx, `UPDATE refresh_tokens SET rotated_at = now() - interval '1 minute' WHERE token_hash = 'A'`); err != nil {
		t.Fatal(err)
	}
	if err := rotate("A", "B3"); !errors.Is(err, store.ErrTokenReused) {
		t.Fatalf("A after the grace period: err = %v, want ErrTokenReused", err)
	}
	for _, hash := range []string{"C", "phone-2"} {
		if err := rotate(hash, hash+"-next"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("token %q after the theft alarm: err = %v, want it revoked", hash, err)
		}
	}
	// Somebody else's session is untouched.
	if err := rotate("other-1", "other-2"); err != nil {
		t.Errorf("another user's token must survive: %v", err)
	}
}

// A token revoked by signing out proves nothing about theft: using it later is just refused and
// must not log the user out of their other devices.
func TestSignedOutTokenIsRefusedWithoutEndingOtherSessions(t *testing.T) {
	url := os.Getenv("AUTH_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("AUTH_TEST_DATABASE_URL is not set")
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
	u := mustUser(t, s, "signout@example.com")
	exp := time.Now().Add(time.Hour)
	for _, h := range []string{"old-phone", "new-phone"} {
		if err := s.SaveRefreshToken(ctx, u.ID, h, exp); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RevokeRefreshToken(ctx, "old-phone"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE refresh_tokens SET revoked_at = now() - interval '1 hour' WHERE token_hash = 'old-phone'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RotateRefreshToken(ctx, "old-phone", "x", exp); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a signed-out token: err = %v, want ErrNotFound", err)
	}
	if _, err := s.RotateRefreshToken(ctx, "new-phone", "y", exp); err != nil {
		t.Fatalf("the other device must stay signed in: %v", err)
	}
}

// mustUser makes a user for a test, with a random Firebase id so no two collide.
func mustUser(t *testing.T, s *store.Store, email string) store.User {
	t.Helper()
	u, err := s.CreateFirebaseUser(context.Background(), email+uuid.NewString()[:6], "Test", uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// UserExists is true for an account and false once it has been deleted, which is how the gateway
// learns that tokens issued before a deletion must stop working.
func TestUserExistsFollowsDeletion(t *testing.T) {
	url := os.Getenv("AUTH_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("AUTH_TEST_DATABASE_URL is not set")
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
	u := mustUser(t, s, "exists@example.com")
	for _, tc := range []struct {
		id   string
		want bool
	}{{u.ID, true}, {uuid.NewString(), false}} {
		if got, err := s.UserExists(ctx, tc.id); err != nil || got != tc.want {
			t.Errorf("UserExists(%s) = %v, %v; want %v", tc.id, got, err, tc.want)
		}
	}
	if err := s.DeleteUser(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.UserExists(ctx, u.ID); got {
		t.Error("the account must not exist after DeleteUser")
	}
}
