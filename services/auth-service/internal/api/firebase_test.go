package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"fitness/auth-service/internal/firebase"
	"fitness/auth-service/internal/token"
)

// fakeVerifier accepts tokens it was told about and refuses the rest.
type fakeVerifier map[string]firebase.User

func (f fakeVerifier) Verify(_ context.Context, raw string) (firebase.User, error) {
	if u, ok := f[raw]; ok {
		return u, nil
	}
	return firebase.User{}, firebase.ErrInvalidToken
}

func TestFirebaseSignInIsOffWithoutAProject(t *testing.T) {
	w := do(newTestRouter(), "POST", "/api/v1/auth/firebase", `{"id_token":"x"}`, nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}

func TestFirebaseSignInRefusesBeforeTouchingTheDatabase(t *testing.T) {
	fb := fakeVerifier{
		"unverified": {UID: "u1", Email: "a@example.com", EmailVerified: false, AuthTime: time.Now()},
		"no-email":   {UID: "u2", EmailVerified: true, AuthTime: time.Now()},
	}
	r := NewRouter(nil, token.NewMaker("test-secret-at-least-16-chars", time.Minute), fb, true)
	cases := []struct {
		name, body string
		want       int
	}{
		{"not json", `nope`, http.StatusBadRequest},
		{"no token", `{}`, http.StatusBadRequest},
		{"a token Firebase did not sign", `{"id_token":"forged"}`, http.StatusUnauthorized},
		{"an unverified email", `{"id_token":"unverified"}`, http.StatusForbidden},
		{"no email at all", `{"id_token":"no-email"}`, http.StatusForbidden},
	}
	for _, tc := range cases {
		if got := do(r, "POST", "/api/v1/auth/firebase", tc.body, nil).Code; got != tc.want {
			t.Errorf("%s: status = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestDisplayName(t *testing.T) {
	long := ""
	for range 100 {
		long += "é"
	}
	cases := []struct {
		fb    firebase.User
		typed string
		want  string
	}{
		{firebase.User{Name: "Asha Rao", Email: "asha@example.com"}, "Typed", "Asha Rao"},
		{firebase.User{Email: "asha@example.com"}, "  Asha  ", "Asha"},
		{firebase.User{Email: "asha.rao@example.com"}, "", "asha.rao"},
		{firebase.User{Name: long}, "", long[:160]}, // 80 two-byte runes
	}
	for _, tc := range cases {
		if got := displayName(tc.fb, tc.typed); got != tc.want {
			t.Errorf("displayName(%+v, %q) = %q, want %q", tc.fb, tc.typed, got, tc.want)
		}
	}
}

func TestOldSignUpAndLoginAreClosedWhenLegacyIsOff(t *testing.T) {
	r := NewRouter(nil, token.NewMaker("test-secret-at-least-16-chars", time.Minute), nil, false)
	for _, path := range []string{"/api/v1/auth/register", "/api/v1/auth/login"} {
		w := do(r, "POST", path, `{"email":"a@example.com","name":"A","password":"long-enough-1"}`, nil)
		if w.Code != http.StatusGone {
			t.Errorf("%s: status = %d, want 410", path, w.Code)
		}
	}
	// Refresh and logout keep working: people already signed in must be able to stay or leave.
	if w := do(r, "POST", "/api/v1/auth/logout", `{}`, nil); w.Code == http.StatusGone {
		t.Error("logout must not be closed")
	}
}

func TestChangingTheNameChecksItFirst(t *testing.T) {
	r := newTestRouter()
	user := map[string]string{"X-User-ID": "11111111-1111-1111-1111-111111111111"}
	long := ""
	for range 81 {
		long += "a"
	}
	cases := []struct {
		name, body string
		headers    map[string]string
		want       int
	}{
		{"not signed in", `{"name":"Asha"}`, nil, http.StatusUnauthorized},
		{"not json", `nope`, user, http.StatusBadRequest},
		{"empty", `{"name":"   "}`, user, http.StatusUnprocessableEntity},
		{"too long", `{"name":"` + long + `"}`, user, http.StatusUnprocessableEntity},
	}
	for _, tc := range cases {
		if got := do(r, "PATCH", "/api/v1/auth/me", tc.body, tc.headers).Code; got != tc.want {
			t.Errorf("%s: status = %d, want %d", tc.name, got, tc.want)
		}
	}
}
