package gateway

import (
	"net/http"
	"testing"
	"time"
)

func TestLimiterAllowsMaxPerWindowThenWaits(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	l := newLimiter(3, time.Minute)
	l.now = func() time.Time { return now }

	for i := range 3 {
		if ok, _ := l.allow("1.2.3.4"); !ok {
			t.Fatalf("attempt %d refused", i+1)
		}
	}
	ok, wait := l.allow("1.2.3.4")
	if ok || wait != time.Minute {
		t.Fatalf("4th attempt: ok=%v wait=%v, want refused for a minute", ok, wait)
	}
	if ok, _ := l.allow("5.6.7.8"); !ok {
		t.Fatal("another client must have its own count")
	}

	now = now.Add(time.Minute)
	if ok, _ := l.allow("1.2.3.4"); !ok {
		t.Fatal("a new window must allow again")
	}
	if len(l.clients) != 1 {
		t.Fatalf("old clients kept: %d, want only the one seen in this window", len(l.clients))
	}
}

func TestSignInIsLimitedPerIP(t *testing.T) {
	svc := fakeService(t)
	g, err := New(Config{
		JWTSecret: secret, AuthURL: svc.URL, WorkoutURL: svc.URL, NutritionURL: svc.URL,
		ActivityURL: svc.URL, SocialURL: svc.URL, AuthAttemptsPerMinute: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	from := func(ip string) map[string]string { return map[string]string{"X-Forwarded-For": ip} }

	// Login, sign-up and Firebase share the count.
	for _, path := range []string{"/api/v1/auth/login", "/api/v1/auth/firebase"} {
		if got := get(g, "POST", path, nil).Code; got != http.StatusOK {
			t.Fatalf("%s: %d", path, got)
		}
	}
	w := get(g, "POST", "/api/v1/auth/register", nil)
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Fatalf("3rd attempt: %d (Retry-After %q), want 429 with Retry-After", w.Code, w.Header().Get("Retry-After"))
	}
	// Refreshing a session is not a guess at a password, so it is not counted.
	if got := get(g, "POST", "/api/v1/auth/refresh", nil).Code; got != http.StatusOK {
		t.Fatalf("refresh: %d, want 200", got)
	}
	// With no trusted proxy, a made-up X-Forwarded-For does not get a fresh count.
	if got := get(g, "POST", "/api/v1/auth/login", from("203.0.113.9")).Code; got != http.StatusTooManyRequests {
		t.Fatalf("spoofed X-Forwarded-For: %d, want 429", got)
	}
}

func TestBehindATrustedProxyEachClientIsCountedApart(t *testing.T) {
	svc := fakeService(t)
	g, err := New(Config{
		JWTSecret: secret, AuthURL: svc.URL, WorkoutURL: svc.URL, NutritionURL: svc.URL,
		ActivityURL: svc.URL, SocialURL: svc.URL, AuthAttemptsPerMinute: 1,
		TrustedProxies: []string{"192.0.2.0/24"}, // httptest requests come from 192.0.2.1
	})
	if err != nil {
		t.Fatal(err)
	}
	from := func(ip string) map[string]string { return map[string]string{"X-Forwarded-For": ip} }
	if got := get(g, "POST", "/api/v1/auth/login", from("198.51.100.1")).Code; got != http.StatusOK {
		t.Fatalf("first client: %d", got)
	}
	if got := get(g, "POST", "/api/v1/auth/login", from("198.51.100.2")).Code; got != http.StatusOK {
		t.Fatalf("second client: %d, want its own count", got)
	}
	if got := get(g, "POST", "/api/v1/auth/login", from("198.51.100.1")).Code; got != http.StatusTooManyRequests {
		t.Fatalf("first client again: %d, want 429", got)
	}
}

func TestBadTrustedProxyIsRefused(t *testing.T) {
	_, err := New(Config{JWTSecret: secret, AuthURL: "http://a", WorkoutURL: "http://a", NutritionURL: "http://a",
		ActivityURL: "http://a", SocialURL: "http://a", TrustedProxies: []string{"not-an-ip"}})
	if err == nil {
		t.Fatal("want an error for a bad TRUSTED_PROXIES entry")
	}
}
