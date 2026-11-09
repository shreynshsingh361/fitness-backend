package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"fitness/auth-service/internal/token"
)

func init() { gin.SetMode(gin.TestMode) }

// These tests never reach the database: the requests are rejected before any
// SQL would run, so the store can be nil.
func newTestRouter() *gin.Engine {
	return NewRouter(nil, token.NewMaker("test-secret-at-least-16-chars", time.Minute), nil, true)
}

func do(r http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestRegisterValidation(t *testing.T) {
	r := newTestRouter()
	cases := []struct {
		name string
		body string
		want int
	}{
		{"not json", `{oops`, http.StatusBadRequest},
		{"bad email", `{"email":"nope","name":"A","password":"longenough1"}`, http.StatusUnprocessableEntity},
		{"short password", `{"email":"a@b.co","name":"A","password":"short"}`, http.StatusUnprocessableEntity},
		{"missing name", `{"email":"a@b.co","name":"  ","password":"longenough1"}`, http.StatusUnprocessableEntity},
		{"display-name email", `{"email":"Bob <bob@b.co>","name":"A","password":"longenough1"}`, http.StatusUnprocessableEntity},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := do(r, "POST", "/api/v1/auth/register", tc.body, nil).Code; got != tc.want {
				t.Fatalf("status = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestMeRequiresUserHeader(t *testing.T) {
	r := newTestRouter()
	if got := do(r, "GET", "/api/v1/auth/me", "", nil).Code; got != http.StatusUnauthorized {
		t.Fatalf("no header: status = %d, want 401", got)
	}
	if got := do(r, "GET", "/api/v1/auth/me", "", map[string]string{"X-User-ID": "not-a-uuid"}).Code; got != http.StatusUnauthorized {
		t.Fatalf("bad header: status = %d, want 401", got)
	}
}

func TestRefreshAndLogoutNeedAToken(t *testing.T) {
	r := newTestRouter()
	for _, path := range []string{"/api/v1/auth/refresh", "/api/v1/auth/logout"} {
		if got := do(r, "POST", path, `{}`, nil).Code; got != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", path, got)
		}
	}
}
