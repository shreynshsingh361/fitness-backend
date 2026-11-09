package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

const (
	secret   = "test-secret-at-least-16-chars"
	userA    = "11111111-1111-1111-1111-111111111111"
	attacker = "99999999-9999-9999-9999-999999999999"
)

func init() { gin.SetMode(gin.TestMode) }

// fakeService stands in for a real backend. It answers every request with the
// headers it received, so tests can check what the gateway actually forwarded.
func fakeService(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"path":       r.URL.RequestURI(),
			"x_user_id":  r.Header.Get("X-User-ID"),
			"x_request":  r.Header.Get("X-Request-ID"),
			"authorized": r.Header.Get("Authorization"),
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

type upstreams struct {
	auth, workout, nutrition, activity, social string
}

func newGatewayWith(t *testing.T, u upstreams) *gin.Engine {
	t.Helper()
	g, err := New(Config{
		JWTSecret: secret, AuthURL: u.auth, WorkoutURL: u.workout, NutritionURL: u.nutrition,
		ActivityURL: u.activity, SocialURL: u.social,
		CORSOrigins: "http://localhost:3000, http://example.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// newGateway is for tests that do not care about the activity and social services.
func newGateway(t *testing.T, auth, workout, nutrition string) *gin.Engine {
	t.Helper()
	return newGatewayWith(t, upstreams{auth: auth, workout: workout, nutrition: nutrition, activity: nutrition, social: nutrition})
}

func tokenFor(t *testing.T, sub, signingSecret string, ttl time.Duration) string {
	t.Helper()
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Subject: sub, Issuer: tokenIssuer, ExpiresAt: jwt.NewNumericDate(time.Now().Add(ttl)),
	}).SignedString([]byte(signingSecret))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func get(h http.Handler, method, path string, headers map[string]string) *httptest.ResponseRecorder {
	// Requests from a real server always carry a cancellable context (it is
	// cancelled when the client disconnects). httptest.NewRequest does not, and
	// without one httputil.ReverseProxy falls back to an old API (CloseNotify)
	// that httptest.ResponseRecorder lacks. So give the test request one.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(method, path, strings.NewReader("")).WithContext(ctx)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func decode(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("response is not JSON: %v\n%s", err, w.Body.String())
	}
	return out
}

func TestProtectedRouteNeedsAValidToken(t *testing.T) {
	svc := fakeService(t)
	g := newGateway(t, svc.URL, svc.URL, svc.URL)

	cases := map[string]map[string]string{
		"no header":        nil,
		"not bearer":       {"Authorization": "Basic abc"},
		"garbage":          {"Authorization": "Bearer nonsense"},
		"expired":          {"Authorization": "Bearer " + tokenFor(t, userA, secret, -time.Minute)},
		"wrong secret":     {"Authorization": "Bearer " + tokenFor(t, userA, "some-other-secret-value", time.Minute)},
		"subject not uuid": {"Authorization": "Bearer " + tokenFor(t, "admin", secret, time.Minute)},
	}
	for name, headers := range cases {
		t.Run(name, func(t *testing.T) {
			if got := get(g, "GET", "/api/v1/workouts", headers).Code; got != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", got)
			}
		})
	}
}

func TestUserIDComesFromTheTokenNeverTheClient(t *testing.T) {
	svc := fakeService(t)
	g := newGateway(t, svc.URL, svc.URL, svc.URL)

	w := get(g, "GET", "/api/v1/workouts?page=2", map[string]string{
		"Authorization": "Bearer " + tokenFor(t, userA, secret, time.Minute),
		"X-User-ID":     attacker, // a malicious client tries to pretend to be someone else
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := decode(t, w)
	if body["x_user_id"] != userA {
		t.Fatalf("service saw X-User-ID %q, want %q (the token's subject)", body["x_user_id"], userA)
	}
	if body["path"] != "/api/v1/workouts?page=2" {
		t.Fatalf("service saw path %q", body["path"])
	}
}

func TestPublicRoutesNeedNoTokenAndStripSpoofedUserID(t *testing.T) {
	svc := fakeService(t)
	g := newGateway(t, svc.URL, svc.URL, svc.URL)

	for _, path := range []string{"/api/v1/auth/login", "/api/v1/auth/register", "/api/v1/auth/firebase", "/api/v1/auth/refresh", "/api/v1/auth/logout"} {
		w := get(g, "POST", path, map[string]string{"X-User-ID": attacker})
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", path, w.Code)
		}
		if got := decode(t, w)["x_user_id"]; got != "" {
			t.Fatalf("%s: service saw X-User-ID %q, want none", path, got)
		}
	}

	// ...but /auth/me is NOT public.
	if got := get(g, "GET", "/api/v1/auth/me", nil).Code; got != http.StatusUnauthorized {
		t.Fatalf("/auth/me without token: status = %d, want 401", got)
	}
}

func TestRoutesGoToTheRightService(t *testing.T) {
	auth, workout, nutrition, activity, social := fakeService(t), fakeService(t), fakeService(t), fakeService(t), fakeService(t)
	hits := map[string]int{}
	count := func(name string, srv *httptest.Server) *httptest.Server {
		inner := srv.Config.Handler
		srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits[name]++
			inner.ServeHTTP(w, r)
		})
		return srv
	}
	count("auth", auth)
	count("workout", workout)
	count("nutrition", nutrition)
	count("activity", activity)
	count("social", social)

	g := newGatewayWith(t, upstreams{auth.URL, workout.URL, nutrition.URL, activity.URL, social.URL})
	h := map[string]string{"Authorization": "Bearer " + tokenFor(t, userA, secret, time.Minute)}

	for path, want := range map[string]string{
		"/api/v1/auth/me":             "auth",
		"/api/v1/exercises":           "workout",
		"/api/v1/workouts/summary":    "workout",
		"/api/v1/records":             "workout",
		"/api/v1/plans/back":          "workout",
		"/api/v1/foods":               "nutrition",
		"/api/v1/recipes":             "nutrition",
		"/api/v1/recipes/x":           "nutrition",
		"/api/v1/diary":               "nutrition",
		"/api/v1/goals":               "nutrition",
		"/api/v1/water":               "nutrition",
		"/api/v1/nutrition/summary":   "nutrition",
		"/api/v1/steps/today":         "activity",
		"/api/v1/steps/stats":         "activity",
		"/api/v1/friends":             "social",
		"/api/v1/friends/invite":      "social",
		"/api/v1/nudges":              "social",
		"/api/v1/groups":              "social",
		"/api/v1/groups/x/report.csv": "social",
	} {
		before := hits[want]
		if code := get(g, "GET", path, h).Code; code != http.StatusOK {
			t.Fatalf("%s: status = %d", path, code)
		}
		if hits[want] != before+1 {
			t.Errorf("%s was not routed to the %s service (hits: %v)", path, want, hits)
		}
	}
}

func TestUnknownPathIs404(t *testing.T) {
	svc := fakeService(t)
	g := newGateway(t, svc.URL, svc.URL, svc.URL)
	h := map[string]string{"Authorization": "Bearer " + tokenFor(t, userA, secret, time.Minute)}
	for _, path := range []string{"/api/v1/nope", "/", "/api/v1/workoutsXYZ", "/internal/anything"} {
		if got := get(g, "GET", path, h).Code; got != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", path, got)
		}
	}
}

func TestNewRoutesNeedAToken(t *testing.T) {
	svc := fakeService(t)
	g := newGateway(t, svc.URL, svc.URL, svc.URL)
	for _, path := range []string{"/api/v1/steps/today", "/api/v1/friends", "/api/v1/nudges", "/api/v1/groups", "/api/v1/groups/x/leaderboard"} {
		if got := get(g, "GET", path, nil).Code; got != http.StatusUnauthorized {
			t.Errorf("%s without a token: status = %d, want 401", path, got)
		}
	}
}

// The services' /internal routes trust whoever calls them, so nothing from outside
// may ever reach them, not even with a valid token.
func TestInternalRoutesAreNeverForwarded(t *testing.T) {
	hit := false
	svc := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	defer svc.Close()
	g := newGatewayWith(t, upstreams{svc.URL, svc.URL, svc.URL, svc.URL, svc.URL})
	h := map[string]string{"Authorization": "Bearer " + tokenFor(t, userA, secret, time.Minute)}

	for _, attempt := range []struct{ method, path string }{
		{"DELETE", "/internal/users/" + userA},
		{"POST", "/internal/steps/totals"},
		{"POST", "/internal/users/lookup"},
		{"DELETE", "/api/v1/internal/users/" + userA},
		// Paths that start with a public prefix, then try to climb out of it.
		{"DELETE", "/api/v1/workouts/../../../internal/users/" + userA},
		{"DELETE", "/api/v1/workouts/%2e%2e/%2e%2e/%2e%2e/internal/users/" + userA},
		{"DELETE", "/api/v1/steps/./../../../internal/users/" + userA},
		{"POST", "/api/v1/auth/login/../../../../internal/users/lookup"},
	} {
		if got := get(g, attempt.method, attempt.path, h).Code; got != http.StatusNotFound {
			t.Errorf("%s %s: status = %d, want 404", attempt.method, attempt.path, got)
		}
		if hit {
			t.Fatalf("%s %s reached a service", attempt.method, attempt.path)
		}
	}
}

func TestHasDotSegment(t *testing.T) {
	for path, want := range map[string]bool{
		"/api/v1/workouts":            false,
		"/api/v1/groups/x/report.csv": false, // a dot inside a name is fine
		"/api/v1/foods/..hidden":      false,
		"/api/v1/../x":                true,
		"/api/v1/./x":                 true,
		"/api/v1/x/..":                true,
	} {
		if got := hasDotSegment(path); got != want {
			t.Errorf("hasDotSegment(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestEveryResponseHasARequestID(t *testing.T) {
	svc := fakeService(t)
	g := newGateway(t, svc.URL, svc.URL, svc.URL)
	w := get(g, "GET", "/health", map[string]string{"X-Request-ID": "client-made-this-up"})
	id := w.Header().Get("X-Request-ID")
	if id == "" || id == "client-made-this-up" {
		t.Fatalf("X-Request-ID = %q, want a fresh server-made id", id)
	}
}

func TestCORS(t *testing.T) {
	svc := fakeService(t)
	g := newGateway(t, svc.URL, svc.URL, svc.URL)

	// Preflight from an allowed origin (the browser sends this before a real call).
	w := get(g, "OPTIONS", "/api/v1/workouts", map[string]string{
		"Origin": "http://localhost:3000", "Access-Control-Request-Method": "POST",
	})
	if w.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:3000" {
		t.Fatalf("Allow-Origin = %q", got)
	}
	if !strings.Contains(w.Header().Get("Access-Control-Allow-Headers"), "Authorization") {
		t.Fatalf("Allow-Headers must include Authorization, got %q", w.Header().Get("Access-Control-Allow-Headers"))
	}
	if !strings.Contains(w.Header().Get("Access-Control-Allow-Methods"), "PATCH") {
		t.Fatalf("Allow-Methods must include PATCH (group settings use it), got %q", w.Header().Get("Access-Control-Allow-Methods"))
	}
	if !strings.Contains(w.Header().Get("Access-Control-Expose-Headers"), "Content-Disposition") {
		t.Fatalf("Expose-Headers must include Content-Disposition (report downloads use it), got %q", w.Header().Get("Access-Control-Expose-Headers"))
	}

	// A second allowed origin from the comma-separated list.
	if got := get(g, "GET", "/health", map[string]string{"Origin": "http://example.test"}).Header().Get("Access-Control-Allow-Origin"); got != "http://example.test" {
		t.Fatalf("second origin: Allow-Origin = %q", got)
	}

	// An origin that is not on the list gets no permission.
	if got := get(g, "GET", "/health", map[string]string{"Origin": "http://evil.test"}).Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("evil origin was allowed: %q", got)
	}
}

func TestDeadServiceGives502NotACrash(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close() // now nothing is listening there

	svc := fakeService(t)
	g := newGateway(t, svc.URL, deadURL, svc.URL)
	w := get(g, "GET", "/api/v1/workouts", map[string]string{"Authorization": "Bearer " + tokenFor(t, userA, secret, time.Minute)})
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", w.Code)
	}
	if decode(t, w)["code"] != "bad_gateway" {
		t.Fatalf("unexpected body: %s", w.Body.String())
	}
}

// summaryService answers like workout/nutrition summary endpoints do.
func summaryService(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-User-ID") != userA {
			http.Error(w, "no user", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDashboardCombinesThreeServices(t *testing.T) {
	workout := summaryService(t, `{"workouts":3,"volume_kg":1200}`)
	nutrition := summaryService(t, `{"calories_eaten":1800,"calories_goal":2200}`)
	activity := summaryService(t, `{"steps":6500,"goal":10000}`)
	g := newGatewayWith(t, upstreams{"http://127.0.0.1:1", workout.URL, nutrition.URL, activity.URL, "http://127.0.0.1:1"})

	w := get(g, "GET", "/api/v1/dashboard?date=2026-11-07", map[string]string{"Authorization": "Bearer " + tokenFor(t, userA, secret, time.Minute)})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	body := decode(t, w)
	if body["date"] != "2026-11-07" {
		t.Errorf("date = %v", body["date"])
	}
	if body["workouts"].(map[string]any)["workouts"] != float64(3) {
		t.Errorf("workouts part wrong: %v", body["workouts"])
	}
	if body["nutrition"].(map[string]any)["calories_goal"] != float64(2200) {
		t.Errorf("nutrition part wrong: %v", body["nutrition"])
	}
	if body["steps"].(map[string]any)["steps"] != float64(6500) {
		t.Errorf("steps part wrong: %v", body["steps"])
	}
	if got := body["unavailable"].([]any); len(got) != 0 {
		t.Errorf("unavailable = %v, want none", got)
	}
}

func TestDashboardSurvivesOneServiceBeingDown(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	nutrition := summaryService(t, `{"calories_eaten":500}`)
	g := newGateway(t, "http://127.0.0.1:1", deadURL, nutrition.URL)

	w := get(g, "GET", "/api/v1/dashboard", map[string]string{"Authorization": "Bearer " + tokenFor(t, userA, secret, time.Minute)})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 even though workouts is down", w.Code)
	}
	body := decode(t, w)
	if body["workouts"] != nil {
		t.Errorf("workouts = %v, want null", body["workouts"])
	}
	if body["nutrition"] == nil {
		t.Errorf("nutrition should still be present")
	}
	unavailable := body["unavailable"].([]any)
	if len(unavailable) != 1 || unavailable[0] != "workouts" {
		t.Errorf("unavailable = %v, want [workouts]", unavailable)
	}
}

func TestDashboardSurvivesTheStepServiceBeingDown(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	workout := summaryService(t, `{"workouts":1}`)
	nutrition := summaryService(t, `{"calories_eaten":500}`)
	g := newGatewayWith(t, upstreams{"http://127.0.0.1:1", workout.URL, nutrition.URL, deadURL, "http://127.0.0.1:1"})

	body := decode(t, get(g, "GET", "/api/v1/dashboard", map[string]string{"Authorization": "Bearer " + tokenFor(t, userA, secret, time.Minute)}))
	if body["steps"] != nil || body["workouts"] == nil || body["nutrition"] == nil {
		t.Errorf("got %v", body)
	}
	if got := body["unavailable"].([]any); len(got) != 1 || got[0] != "steps" {
		t.Errorf("unavailable = %v, want [steps]", got)
	}
}

func TestDashboardChecksAuthAndDate(t *testing.T) {
	svc := fakeService(t)
	g := newGateway(t, svc.URL, svc.URL, svc.URL)
	if got := get(g, "GET", "/api/v1/dashboard", nil).Code; got != http.StatusUnauthorized {
		t.Errorf("no token: status = %d, want 401", got)
	}
	h := map[string]string{"Authorization": "Bearer " + tokenFor(t, userA, secret, time.Minute)}
	if got := get(g, "GET", "/api/v1/dashboard?date=garbage", h).Code; got != http.StatusUnprocessableEntity {
		t.Errorf("bad date: status = %d, want 422", got)
	}
}

func TestServiceHealth(t *testing.T) {
	up := fakeService(t)
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()

	g := newGateway(t, up.URL, deadURL, up.URL)
	body := decode(t, get(g, "GET", "/health/services", nil))
	if body["status"] != "degraded" {
		t.Errorf("status = %v, want degraded", body["status"])
	}
	services := body["services"].(map[string]any)
	if services["workout"] != "down" || services["auth"] != "up" || services["nutrition"] != "up" ||
		services["activity"] != "up" || services["social"] != "up" {
		t.Errorf("services = %v", services)
	}
}

func TestNewRejectsBadUpstreamURLs(t *testing.T) {
	good := "http://a"
	for name, cfg := range map[string]Config{
		"auth":      {AuthURL: "not a url", WorkoutURL: good, NutritionURL: good, ActivityURL: good, SocialURL: good},
		"workout":   {AuthURL: good, WorkoutURL: "nope", NutritionURL: good, ActivityURL: good, SocialURL: good},
		"nutrition": {AuthURL: good, WorkoutURL: good, NutritionURL: "nope", ActivityURL: good, SocialURL: good},
		"activity":  {AuthURL: good, WorkoutURL: good, NutritionURL: good, ActivityURL: "nope", SocialURL: good},
		"social":    {AuthURL: good, WorkoutURL: good, NutritionURL: good, ActivityURL: good, SocialURL: "nope"},
	} {
		cfg.JWTSecret = secret
		if _, err := New(cfg); err == nil {
			t.Errorf("expected an error for a bad %s URL", name)
		}
	}
}
