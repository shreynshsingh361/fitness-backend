// Package token creates and checks the two kinds of credentials we hand out:
//
//   - access token:  a short-lived JWT (15 minutes). The gateway checks it on
//     every request.
//   - refresh token: a long random string (30 days). It can only be traded for
//     a new pair of tokens at /auth/refresh.
package token

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const Issuer = "fitness-auth"

// KeyID names a signing secret without revealing it: the first eight hex digits of its SHA-256.
// It goes in each token's "kid" header, so the gateway can tell which secret signed it while a
// secret is being rotated out. The gateway has its own copy of this function.
func KeyID(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:4])
}

// Maker signs and checks access tokens with one secret.
type Maker struct {
	secret    []byte
	keyID     string
	accessTTL time.Duration
}

// NewMaker makes a Maker that signs with secret and gives tokens accessTTL to live.
func NewMaker(secret string, accessTTL time.Duration) *Maker {
	return &Maker{secret: []byte(secret), keyID: KeyID(secret), accessTTL: accessTTL}
}

// AccessTTL is how long a new access token stays valid.
func (m *Maker) AccessTTL() time.Duration { return m.accessTTL }

// NewAccessToken signs a JWT whose "sub" (subject) is the user's id.
func (m *Maker) NewAccessToken(userID string) (string, error) {
	now := time.Now()
	claims := jwt.RegisteredClaims{
		Subject:   userID,
		Issuer:    Issuer,
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(m.accessTTL)),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	token.Header["kid"] = m.keyID
	return token.SignedString(m.secret)
}

// Parse checks the signature, issuer and expiry and returns the user id.
func (m *Maker) Parse(tokenString string) (string, error) {
	claims := &jwt.RegisteredClaims{}
	_, err := jwt.ParseWithClaims(tokenString, claims,
		func(*jwt.Token) (any, error) { return m.secret, nil },
		jwt.WithValidMethods([]string{"HS256"}), // refuse "alg: none" and friends
		jwt.WithIssuer(Issuer),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return "", err
	}
	if claims.Subject == "" {
		return "", errors.New("token has no subject")
	}
	return claims.Subject, nil
}

// NewRefreshToken returns the token to give the client and the hash to store.
func NewRefreshToken() (raw, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	raw = base64.RawURLEncoding.EncodeToString(b)
	return raw, HashRefreshToken(raw), nil
}

// HashRefreshToken is what is stored in the database instead of the refresh token itself, so a
// leaked database does not leak working sessions.
func HashRefreshToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
