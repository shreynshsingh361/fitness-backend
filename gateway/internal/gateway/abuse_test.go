package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// bearer is a signed-in user's header for the tests below.
func bearer(t *testing.T, userID string) map[string]string {
	t.Helper()
	return map[string]string{"Authorization": "Bearer " + tokenFor(t, userID, secret, time.Minute)}
}

// Break #2: the oversized request. A buggy client, a curious user with curl, or someone testing the
// limits sends a huge body. The gateway is the only door, so it must turn the body away before
// reading it all into memory, and answer 413 (Payload Too Large), not forward it.
func TestBreak2OversizedBodyIsTurnedAway(t *testing.T) {
	svc := fakeService(t)
	g := newGateway(t, svc.URL, svc.URL, svc.URL)
	huge := strings.Repeat("a", 5<<20) // 5 MiB

	for _, path := range []string{"/api/v1/auth/login", "/api/v1/diary", "/api/v1/steps/sync"} {
		w := postJSON(g, path, `{"x":"`+huge+`"}`, bearer(t, userA))
		if w.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("%s with a 5 MiB body: status = %d, want 413", path, w.Code)
		}
	}
	// Ordinary requests are not hurt by the limit.
	if w := postJSON(g, "/api/v1/diary", `{"food_id":"x"}`, bearer(t, userA)); w.Code != http.StatusOK {
		t.Errorf("a small body: status = %d, want 200", w.Code)
	}
}

// A server that waits for ever on a slow or silent client lets a handful of connections starve
// everyone else (slowloris). The server must have limits on reading, writing and idling.
func TestBreak2ServerGivesUpOnSlowClients(t *testing.T) {
	srv := NewServer(":0", http.NotFoundHandler())
	for name, d := range map[string]time.Duration{
		"ReadHeaderTimeout": srv.ReadHeaderTimeout, "ReadTimeout": srv.ReadTimeout,
		"WriteTimeout": srv.WriteTimeout, "IdleTimeout": srv.IdleTimeout,
	} {
		if d <= 0 {
			t.Errorf("%s is not set: a slow client could hold a connection for ever", name)
		}
	}
}

// Break #4: the hammer. A signed-in user (or a script using their token) calls an expensive
// endpoint in a loop. The barcode lookup makes the server call a third party, so it must be
// throttled per user, and one person's flood must not lock anyone else out.
func TestBreak4HammeringOneUserIsThrottledAndOthersAreNot(t *testing.T) {
	svc := fakeService(t)
	g := newGateway(t, svc.URL, svc.URL, svc.URL)
	hammer := bearer(t, userA)

	var ok, limited int
	for range 200 {
		switch get(g, "GET", "/api/v1/foods/barcode/5449000000996", hammer).Code {
		case http.StatusOK:
			ok++
		case http.StatusTooManyRequests:
			limited++
		}
	}
	if limited == 0 || ok > 60 {
		t.Fatalf("200 barcode lookups in a burst: %d went through, %d were throttled; want a hard cap well under 60", ok, limited)
	}
	if w := get(g, "GET", "/api/v1/foods/barcode/5449000000996", hammer); w.Header().Get("Retry-After") == "" {
		t.Error("a throttled answer must say when to come back (Retry-After)")
	}
	// Someone else is not affected by the flood.
	if got := get(g, "GET", "/api/v1/foods/barcode/5449000000996", bearer(t, attacker)).Code; got != http.StatusOK {
		t.Errorf("another user during the flood: status = %d, want 200", got)
	}
}

// Everything else a signed-in user does has a generous ceiling too, so one runaway client cannot
// use the whole server. The app itself never comes near it.
func TestBreak4RunawayClientHitsTheOverallCeiling(t *testing.T) {
	svc := fakeService(t)
	g := newGateway(t, svc.URL, svc.URL, svc.URL)
	runaway := bearer(t, userA)
	limited := 0
	for range 1500 {
		if get(g, "GET", "/api/v1/foods?search=a", runaway).Code == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited == 0 {
		t.Fatal("1,500 requests in a burst from one user were all accepted: there is no overall ceiling")
	}
}

// /ready is 200 only when every service is ready; one that is alive but cannot reach its
// database (503 on /ready) makes the whole system "not ready", and the answer names it.
func TestReadyNeedsEveryService(t *testing.T) {
	ok := fakeService(t)
	sick := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ready" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(sick.Close)

	all := newGateway(t, ok.URL, ok.URL, ok.URL)
	if got := get(all, "GET", "/ready", nil).Code; got != http.StatusOK {
		t.Fatalf("every service ready: status = %d, want 200", got)
	}
	one := newGateway(t, ok.URL, ok.URL, sick.URL) // the nutrition service cannot reach its database
	w := get(one, "GET", "/ready", nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("one service not ready: status = %d, want 503", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"nutrition":"down"`) {
		t.Errorf("the answer must name the service that is down: %s", w.Body.String())
	}
	if got := get(one, "GET", "/health", nil).Code; got != http.StatusOK {
		t.Errorf("/health is about the gateway itself and must stay 200, got %d", got)
	}
}

// With no allowed origins (production's default) a browser page from anywhere, localhost included,
// gets no CORS permission; with an origin listed, only that origin does.
func TestCORSAllowsOnlyTheListedOrigins(t *testing.T) {
	svc := fakeService(t)
	build := func(origins string) http.Handler {
		g, err := New(Config{JWTSecret: secret, AuthURL: svc.URL, WorkoutURL: svc.URL, NutritionURL: svc.URL,
			ActivityURL: svc.URL, SocialURL: svc.URL, CORSOrigins: origins})
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	allowed := func(g http.Handler, origin string) string {
		return get(g, "GET", "/health", map[string]string{"Origin": origin}).Header().Get("Access-Control-Allow-Origin")
	}
	none := build("")
	for _, origin := range []string{"http://localhost:3000", "https://evil.example", ""} {
		if got := allowed(none, origin); got != "" {
			t.Errorf("no origins configured, Origin %q: got Allow-Origin %q, want none", origin, got)
		}
	}
	one := build("https://app.example.com")
	if got := allowed(one, "https://app.example.com"); got != "https://app.example.com" {
		t.Errorf("the listed origin: got %q", got)
	}
	if got := allowed(one, "http://localhost:3000"); got != "" {
		t.Errorf("an unlisted origin: got %q, want none", got)
	}
}
