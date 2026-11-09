package gateway

import (
	"net/http"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	oldSecret = "the-old-secret-used-until-the-rotation"
	newSecret = "the-new-secret-after-the-rotation-xx"
)

// signedWith makes a user's token signed with secret, carrying the key id the auth service would.
func signedWith(t *testing.T, secret string, withKeyID bool) map[string]string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Subject: userA, Issuer: tokenIssuer, ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
	})
	if withKeyID {
		tok.Header["kid"] = keyID(secret)
	}
	s, err := tok.SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{"Authorization": "Bearer " + s}
}

// newGatewayRotating builds a gateway whose current secret is newSecret and whose previous one is
// previous (empty = no rotation under way).
func newGatewayRotating(t *testing.T, previous string) http.Handler {
	t.Helper()
	svc := fakeService(t)
	g, err := New(Config{
		JWTSecret: newSecret, JWTSecretPrevious: previous, AuthURL: svc.URL, WorkoutURL: svc.URL,
		NutritionURL: svc.URL, ActivityURL: svc.URL, SocialURL: svc.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// Rotating the secret must not log everybody out: during the rotation tokens signed with the old
// secret still work (they live 15 minutes), and afterwards they stop working.
func TestSecretRotationAcceptsThePreviousSecretOnlyWhileItIsSet(t *testing.T) {
	status := func(g http.Handler, secret string, withKeyID bool) int {
		return get(g, "GET", "/api/v1/steps/today", signedWith(t, secret, withKeyID)).Code
	}

	rotating := newGatewayRotating(t, oldSecret)
	if got := status(rotating, newSecret, true); got != http.StatusOK {
		t.Errorf("token with the new secret: %d, want 200", got)
	}
	if got := status(rotating, oldSecret, true); got != http.StatusOK {
		t.Errorf("token with the old secret during the rotation: %d, want 200", got)
	}

	finished := newGatewayRotating(t, "") // the previous secret was removed from the settings
	if got := status(finished, oldSecret, true); got != http.StatusUnauthorized {
		t.Errorf("token with the old secret after the rotation: %d, want 401", got)
	}
	if got := status(finished, newSecret, true); got != http.StatusOK {
		t.Errorf("token with the new secret after the rotation: %d, want 200", got)
	}
}

// A token that names a key the gateway does not know, or is signed with a secret nobody
// configured, is refused like any forgery; a token with no key id still works with the current
// secret (the ones issued before key ids existed).
func TestForgedAndKeylessTokens(t *testing.T) {
	g := newGatewayRotating(t, oldSecret)
	check := func(name string, headers map[string]string, want int) {
		t.Helper()
		if got := get(g, "GET", "/api/v1/steps/today", headers).Code; got != want {
			t.Errorf("%s: %d, want %d", name, got, want)
		}
	}
	check("signed with an unknown secret, key id forged to look like the current one",
		func() map[string]string {
			tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
				Subject: userA, Issuer: tokenIssuer, ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
			})
			tok.Header["kid"] = keyID(newSecret)
			s, _ := tok.SignedString([]byte("an-attacker-guess-of-the-secret!!"))
			return map[string]string{"Authorization": "Bearer " + s}
		}(), http.StatusUnauthorized)
	check("no key id, current secret", signedWith(t, newSecret, false), http.StatusOK)
	check("no key id, unknown secret", signedWith(t, "an-attacker-guess-of-the-secret!!", false), http.StatusUnauthorized)
	check("an unknown key id", func() map[string]string {
		h := signedWith(t, newSecret, true)
		return map[string]string{"Authorization": h["Authorization"] + "x"}
	}(), http.StatusUnauthorized)
}

// The key id is a stable name for a secret, different for different secrets, and does not contain it.
func TestKeyID(t *testing.T) {
	if keyID(oldSecret) == keyID(newSecret) {
		t.Error("two secrets must have two key ids")
	}
	if got := keyID(newSecret); len(got) != 8 || got != keyID(newSecret) {
		t.Errorf("key id = %q, want 8 stable hex digits", got)
	}
}
