// Package firebase checks the ID tokens that Firebase Authentication gives the app
// after someone signs in (email and password, or Google).
//
// The app sends that token to POST /auth/firebase. This package proves it is real:
// it was signed by Google, it is meant for our Firebase project, and it has not
// expired. Only then does the auth service hand out its own tokens.
package firebase

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// CertsURL is where Google publishes the public keys Firebase signs ID tokens with.
const CertsURL = "https://www.googleapis.com/robot/v1/metadata/x509/securetoken@system.gserviceaccount.com"

var ErrInvalidToken = errors.New("firebase id token is invalid")

// User is what a verified ID token tells us about the person.
type User struct {
	UID           string
	Email         string
	EmailVerified bool
	Name          string
	Provider      string    // "password", "google.com", ...
	AuthTime      time.Time // when the person last typed their password or picked their Google account
}

// KeySource returns Google's current public keys by key id, and how long they may be kept.
type KeySource func(ctx context.Context) (keys map[string]*rsa.PublicKey, maxAge time.Duration, err error)

type Verifier struct {
	projectID string
	source    KeySource
	now       func() time.Time

	mu        sync.Mutex
	keys      map[string]*rsa.PublicKey
	expires   time.Time
	lastFetch time.Time
}

// refetchGap stops tokens with made-up key ids from making us download Google's
// keys on every request.
const refetchGap = time.Minute

// NewVerifier checks tokens for one Firebase project. source is usually GoogleKeys.
func NewVerifier(projectID string, source KeySource) *Verifier {
	return &Verifier{projectID: projectID, source: source, now: time.Now}
}

type claims struct {
	jwt.RegisteredClaims
	AuthTime      int64  `json:"auth_time"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
	Firebase      struct {
		SignInProvider string `json:"sign_in_provider"`
	} `json:"firebase"`
}

// Verify follows Firebase's rules for checking an ID token by hand:
// RS256 signed by one of Google's current keys, aud = project id,
// iss = https://securetoken.google.com/<project id>, not expired, with a subject.
func (v *Verifier) Verify(ctx context.Context, raw string) (User, error) {
	var c claims
	parsed, err := jwt.ParseWithClaims(raw, &c, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		return v.key(ctx, kid)
	},
		jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithAudience(v.projectID),
		jwt.WithIssuer("https://securetoken.google.com/"+v.projectID),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithTimeFunc(v.now),
		jwt.WithLeeway(30*time.Second),
	)
	if err != nil || !parsed.Valid {
		return User{}, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}
	if c.Subject == "" || len(c.Subject) > 128 {
		return User{}, fmt.Errorf("%w: no subject", ErrInvalidToken)
	}
	if c.AuthTime == 0 || time.Unix(c.AuthTime, 0).After(v.now().Add(30*time.Second)) {
		return User{}, fmt.Errorf("%w: bad auth_time", ErrInvalidToken)
	}
	return User{
		UID:           c.Subject,
		Email:         strings.ToLower(strings.TrimSpace(c.Email)),
		EmailVerified: c.EmailVerified,
		Name:          strings.TrimSpace(c.Name),
		Provider:      c.Firebase.SignInProvider,
		AuthTime:      time.Unix(c.AuthTime, 0),
	}, nil
}

// key finds the public key for kid, fetching Google's keys again when the cache
// has expired or the kid is new (Google rotates its keys every few hours).
func (v *Verifier) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	fresh := v.now().Before(v.expires)
	if k, ok := v.keys[kid]; ok && fresh {
		return k, nil
	}
	if fresh && v.now().Sub(v.lastFetch) < refetchGap {
		return nil, errors.New("unknown key id")
	}
	v.lastFetch = v.now()
	keys, maxAge, err := v.source(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetching google keys: %w", err)
	}
	v.keys, v.expires = keys, v.now().Add(maxAge)
	if k, ok := keys[kid]; ok {
		return k, nil
	}
	return nil, errors.New("unknown key id")
}

// GoogleKeys downloads the keys from CertsURL. Google says how long to keep them
// in the Cache-Control max-age header.
func GoogleKeys(client *http.Client) KeySource {
	return func(ctx context.Context) (map[string]*rsa.PublicKey, time.Duration, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, CertsURL, nil)
		if err != nil {
			return nil, 0, err
		}
		res, err := client.Do(req)
		if err != nil {
			return nil, 0, err
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return nil, 0, fmt.Errorf("google keys: status %d", res.StatusCode)
		}
		var certs map[string]string
		if err := json.NewDecoder(res.Body).Decode(&certs); err != nil {
			return nil, 0, err
		}
		keys, err := ParseCerts(certs)
		return keys, maxAge(res.Header.Get("Cache-Control")), err
	}
}

// ParseCerts turns {kid: PEM certificate} into public keys.
func ParseCerts(certs map[string]string) (map[string]*rsa.PublicKey, error) {
	keys := map[string]*rsa.PublicKey{}
	for kid, text := range certs {
		block, _ := pem.Decode([]byte(text))
		if block == nil {
			return nil, fmt.Errorf("key %s: not PEM", kid)
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("key %s: %w", kid, err)
		}
		pub, ok := cert.PublicKey.(*rsa.PublicKey)
		if !ok {
			return nil, fmt.Errorf("key %s: not RSA", kid)
		}
		keys[kid] = pub
	}
	return keys, nil
}

// maxAge reads max-age from Cache-Control; an hour if it is missing.
func maxAge(cacheControl string) time.Duration {
	for _, part := range strings.Split(cacheControl, ",") {
		part = strings.TrimSpace(part)
		if v, ok := strings.CutPrefix(part, "max-age="); ok {
			if s, err := strconv.Atoi(v); err == nil && s > 0 {
				return time.Duration(s) * time.Second
			}
		}
	}
	return time.Hour
}
