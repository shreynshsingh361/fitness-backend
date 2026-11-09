package token

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const secret = "test-secret-at-least-16-chars"

func TestAccessTokenRoundTrip(t *testing.T) {
	m := NewMaker(secret, time.Minute)
	tok, err := m.NewAccessToken("user-123")
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.Parse(tok)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got != "user-123" {
		t.Fatalf("subject = %q, want user-123", got)
	}
}

func TestParseRejectsBadTokens(t *testing.T) {
	m := NewMaker(secret, time.Minute)

	expired, _ := NewMaker(secret, -time.Minute).NewAccessToken("u")
	otherSecret, _ := NewMaker("a-completely-different-secret", time.Minute).NewAccessToken("u")
	unsigned, _ := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.RegisteredClaims{
		Subject: "u", Issuer: Issuer, ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	}).SignedString(jwt.UnsafeAllowNoneSignatureType)

	cases := map[string]string{
		"expired":      expired,
		"wrong secret": otherSecret,
		"alg none":     unsigned,
		"garbage":      "not.a.jwt",
		"empty":        "",
	}
	for name, tok := range cases {
		if _, err := m.Parse(tok); err == nil {
			t.Errorf("%s: expected an error, got none", name)
		}
	}
}

func TestRefreshTokens(t *testing.T) {
	raw, hash, err := NewRefreshToken()
	if err != nil {
		t.Fatal(err)
	}
	if raw == hash || HashRefreshToken(raw) != hash {
		t.Fatal("hash must be derived from the raw token and differ from it")
	}
	raw2, _, _ := NewRefreshToken()
	if raw == raw2 {
		t.Fatal("refresh tokens must be unique")
	}
}
