package firebase

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const project = "hxvy-test"

var now = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

func newKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func goodClaims() jwt.MapClaims {
	return jwt.MapClaims{
		"iss":            "https://securetoken.google.com/" + project,
		"aud":            project,
		"sub":            "firebase-uid-1",
		"iat":            now.Add(-time.Minute).Unix(),
		"exp":            now.Add(time.Hour).Unix(),
		"auth_time":      now.Add(-time.Minute).Unix(),
		"email":          "Asha@Example.com",
		"email_verified": true,
		"name":           "Asha",
		"firebase":       map[string]any{"sign_in_provider": "google.com"},
	}
}

func sign(t *testing.T, key *rsa.PrivateKey, kid string, c jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, c)
	tok.Header["kid"] = kid
	s, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func verifier(keys map[string]*rsa.PublicKey, fetches *int) *Verifier {
	v := NewVerifier(project, func(context.Context) (map[string]*rsa.PublicKey, time.Duration, error) {
		*fetches++
		return keys, time.Hour, nil
	})
	v.now = func() time.Time { return now }
	return v
}

func TestAGoodTokenGivesTheUser(t *testing.T) {
	key := newKey(t)
	fetches := 0
	v := verifier(map[string]*rsa.PublicKey{"k1": &key.PublicKey}, &fetches)

	u, err := v.Verify(context.Background(), sign(t, key, "k1", goodClaims()))
	if err != nil {
		t.Fatal(err)
	}
	want := User{UID: "firebase-uid-1", Email: "asha@example.com", EmailVerified: true, Name: "Asha", Provider: "google.com",
		AuthTime: time.Unix(now.Add(-time.Minute).Unix(), 0)}
	if u != want {
		t.Fatalf("got %+v, want %+v", u, want)
	}

	// The keys are kept, not downloaded for every sign-in.
	if _, err := v.Verify(context.Background(), sign(t, key, "k1", goodClaims())); err != nil {
		t.Fatal(err)
	}
	if fetches != 1 {
		t.Fatalf("keys fetched %d times, want 1", fetches)
	}
}

func TestBadTokensAreRefused(t *testing.T) {
	key := newKey(t)
	other := newKey(t)
	keys := map[string]*rsa.PublicKey{"k1": &key.PublicKey}

	with := func(change func(jwt.MapClaims)) jwt.MapClaims {
		c := goodClaims()
		change(c)
		return c
	}
	hs256, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, goodClaims()).SignedString([]byte("guessable-secret"))

	cases := map[string]string{
		"signed by someone else":   sign(t, other, "k1", goodClaims()),
		"unknown key id":           sign(t, key, "k9", goodClaims()),
		"another firebase project": sign(t, key, "k1", with(func(c jwt.MapClaims) { c["aud"] = "someone-else" })),
		"wrong issuer":             sign(t, key, "k1", with(func(c jwt.MapClaims) { c["iss"] = "https://evil.example" })),
		"expired":                  sign(t, key, "k1", with(func(c jwt.MapClaims) { c["exp"] = now.Add(-time.Hour).Unix() })),
		"no expiry":                sign(t, key, "k1", with(func(c jwt.MapClaims) { delete(c, "exp") })),
		"issued in the future":     sign(t, key, "k1", with(func(c jwt.MapClaims) { c["iat"] = now.Add(time.Hour).Unix() })),
		"no subject":               sign(t, key, "k1", with(func(c jwt.MapClaims) { c["sub"] = "" })),
		"no auth_time":             sign(t, key, "k1", with(func(c jwt.MapClaims) { delete(c, "auth_time") })),
		"signed with HS256":        hs256,
		"garbage":                  "not.a.token",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			fetches := 0
			_, err := verifier(keys, &fetches).Verify(context.Background(), raw)
			if !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("err = %v, want ErrInvalidToken", err)
			}
		})
	}
}

func TestANewKeyIdFetchesTheKeysAgain(t *testing.T) {
	oldKey, newerKey := newKey(t), newKey(t)
	keys := map[string]*rsa.PublicKey{"old": &oldKey.PublicKey}
	fetches := 0
	v := verifier(nil, &fetches)
	v.source = func(context.Context) (map[string]*rsa.PublicKey, time.Duration, error) {
		fetches++
		return keys, time.Hour, nil
	}
	if _, err := v.Verify(context.Background(), sign(t, oldKey, "old", goodClaims())); err != nil {
		t.Fatal(err)
	}
	keys = map[string]*rsa.PublicKey{"old": &oldKey.PublicKey, "new": &newerKey.PublicKey} // Google rotated
	if _, err := v.Verify(context.Background(), sign(t, newerKey, "new", goodClaims())); err == nil {
		t.Fatal("keys were fetched again within a minute")
	}
	now = now.Add(2 * time.Minute)
	defer func() { now = now.Add(-2 * time.Minute) }()
	if _, err := v.Verify(context.Background(), sign(t, newerKey, "new", goodClaims())); err != nil {
		t.Fatal(err)
	}
	if fetches != 2 {
		t.Fatalf("fetches = %d, want 2", fetches)
	}
}

func TestMaxAge(t *testing.T) {
	cases := map[string]time.Duration{
		"public, max-age=19302, must-revalidate, no-transform": 19302 * time.Second,
		"":            time.Hour,
		"max-age=abc": time.Hour,
	}
	for in, want := range cases {
		if got := maxAge(in); got != want {
			t.Errorf("maxAge(%q) = %v, want %v", in, got, want)
		}
	}
}
