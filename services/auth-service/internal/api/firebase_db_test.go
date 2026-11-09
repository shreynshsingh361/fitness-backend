package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"fitness/auth-service/internal/database"
	"fitness/auth-service/internal/store"
	"fitness/auth-service/internal/token"
)

// These run against a real, empty Postgres: set AUTH_TEST_DATABASE_URL to one.
// Never point it at a database with real accounts in it.
func TestFirebaseSignInWithADatabase(t *testing.T) {
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

	hash, _ := bcrypt.GenerateFromPassword([]byte("old-password-1"), bcrypt.MinCost)
	old, err := s.CreateUser(ctx, "old@example.com", "Old Friend", string(hash))
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	var oldSession authResponse
	{
		r := NewRouter(s, token.NewMaker("test-secret-at-least-16-chars", time.Minute), nil, true)
		w := do(r, "POST", "/api/v1/auth/login", `{"email":"old@example.com","password":"old-password-1"}`, nil)
		_ = json.Unmarshal(w.Body.Bytes(), &oldSession)
		if w.Code != http.StatusOK || oldSession.RefreshToken == "" {
			t.Fatalf("old login before linking: %d", w.Code)
		}
	}
	fb := fakeVerifier{
		"google-new":  {UID: "g-new", Email: "new@example.com", EmailVerified: true, Name: "New Person", AuthTime: now},
		"old-by-mail": {UID: "g-old", Email: "old@example.com", EmailVerified: true, AuthTime: now},
		"thief":       {UID: "g-thief", Email: "old@example.com", EmailVerified: true, AuthTime: now},
		"stale":       {UID: "g-new", Email: "new@example.com", EmailVerified: true, AuthTime: now.Add(-time.Hour)},
		"typed-name":  {UID: "p-typed", Email: "typed@example.com", EmailVerified: true, AuthTime: now},
	}
	r := NewRouter(s, token.NewMaker("test-secret-at-least-16-chars", time.Minute), fb, true)

	signIn := func(body string) (int, authResponse) {
		w := do(r, "POST", "/api/v1/auth/firebase", body, nil)
		var res authResponse
		_ = json.Unmarshal(w.Body.Bytes(), &res)
		return w.Code, res
	}

	// A new person gets a new account, named by Google.
	code, first := signIn(`{"id_token":"google-new"}`)
	if code != http.StatusCreated || first.User.Name != "New Person" || first.AccessToken == "" || first.RefreshToken == "" {
		t.Fatalf("new person: %d %+v", code, first)
	}
	// Next time it is the same account.
	code, again := signIn(`{"id_token":"google-new"}`)
	if code != http.StatusOK || again.User.ID != first.User.ID {
		t.Fatalf("second sign-in: %d %+v, want the same user %s", code, again.User, first.User.ID)
	}

	// Someone from before Firebase keeps their account (and everything in it).
	code, linked := signIn(`{"id_token":"old-by-mail"}`)
	if code != http.StatusOK || linked.User.ID != old.ID {
		t.Fatalf("old account: %d %+v, want user %s", code, linked.User, old.ID)
	}
	// The old sign-up never checked the email, so whoever made the account may not own
	// it. Once the real owner arrives, the old password and old sessions stop working.
	w := do(r, "POST", "/api/v1/auth/login", `{"email":"old@example.com","password":"old-password-1"}`, nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("old password after linking: %d, want 401", w.Code)
	}
	if w := do(r, "POST", "/api/v1/auth/refresh", `{"refresh_token":"`+oldSession.RefreshToken+`"}`, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("old session after linking: %d, want 401", w.Code)
	}
	// A second Firebase user with the same email cannot take the account over.
	if code, _ := signIn(`{"id_token":"thief"}`); code != http.StatusConflict {
		t.Fatalf("second firebase user on the same email: %d, want 409", code)
	}

	// With no name from Firebase the typed name is used.
	if _, res := signIn(`{"id_token":"typed-name","name":"Typed Name"}`); res.User.Name != "Typed Name" {
		t.Fatalf("typed name: %+v", res.User)
	}

	// A Firebase-only account has no password, so nothing logs in with one.
	for _, pw := range []string{"", "anything-at-all"} {
		body, _ := json.Marshal(map[string]string{"email": "new@example.com", "password": pw})
		if w := do(r, "POST", "/api/v1/auth/login", string(body), nil); w.Code != http.StatusUnauthorized {
			t.Fatalf("password %q on a Firebase-only account: %d, want 401", pw, w.Code)
		}
	}

	// Deleting a Firebase-only account needs a fresh Firebase sign-in by that same person.
	verify := func(id, body string) int {
		return do(r, "POST", "/internal/users/"+id+"/verify-password", body, nil).Code
	}
	id := first.User.ID
	for name, tc := range map[string]struct {
		body string
		want int
	}{
		"a fresh sign-in":        {`{"firebase_id_token":"google-new"}`, http.StatusNoContent},
		"a sign-in an hour old":  {`{"firebase_id_token":"stale"}`, http.StatusUnauthorized},
		"someone else's sign-in": {`{"firebase_id_token":"typed-name"}`, http.StatusUnauthorized},
		"a forged token":         {`{"firebase_id_token":"forged"}`, http.StatusUnauthorized},
		"an empty password":      {`{"password":""}`, http.StatusUnauthorized},
		"nothing":                {`{}`, http.StatusUnauthorized},
	} {
		if got := verify(id, tc.body); got != tc.want {
			t.Errorf("verify %s: %d, want %d", name, got, tc.want)
		}
	}
	// The linked old account deletes with Firebase now, not the wiped password.
	if got := verify(old.ID, `{"password":"old-password-1"}`); got != http.StatusUnauthorized {
		t.Errorf("old account with its wiped password: %d, want 401", got)
	}
	if got := verify(old.ID, `{"firebase_id_token":"old-by-mail"}`); got != http.StatusNoContent {
		t.Errorf("old account with firebase: %d", got)
	}
}
