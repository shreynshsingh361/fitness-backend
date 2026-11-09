package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// callLog records which service was asked what, in order, across goroutines. The gateway's own
// "does this account still exist?" check on every signed-in request is kept apart (see checks):
// the tests below are about what account deletion does, not about that guard.
type callLog struct {
	mu     sync.Mutex
	calls  []string
	checks []string
}

// add records one call; the existence check goes to its own list.
func (l *callLog) add(call string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if strings.HasSuffix(call, "/exists") {
		l.checks = append(l.checks, call)
		return
	}
	l.calls = append(l.calls, call)
}

// all returns the calls made so far, not counting existence checks.
func (l *callLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.calls)
}

// existenceChecks returns the "does this account still exist?" calls made so far.
func (l *callLog) existenceChecks() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.checks)
}

// accountService is a fake service that records calls and answers with a fixed status.
func accountService(t *testing.T, name string, log *callLog, status int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(name + " " + r.Method + " " + r.URL.Path)
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func postJSON(h http.Handler, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

const (
	verifyCall     = "auth POST /internal/users/" + userA + "/verify-password"
	deleteAuthCall = "auth DELETE /internal/users/" + userA
)

func tokenHeader(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{"Authorization": "Bearer " + tokenFor(t, userA, secret, time.Minute)}
}

func allUp(t *testing.T, log *callLog) upstreams {
	t.Helper()
	return upstreams{
		workout:   accountService(t, "workout", log, http.StatusNoContent),
		nutrition: accountService(t, "nutrition", log, http.StatusNoContent),
		activity:  accountService(t, "activity", log, http.StatusNoContent),
		social:    accountService(t, "social", log, http.StatusNoContent),
		auth:      accountService(t, "auth", log, http.StatusNoContent),
	}
}

func TestDeleteAccountRemovesDataFirstAndTheLoginLast(t *testing.T) {
	log := &callLog{}
	g := newGatewayWith(t, allUp(t, log))

	w := postJSON(g, "/api/v1/account/delete", `{"password":"secret-pass"}`, tokenHeader(t))
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}

	calls := log.all()
	if len(calls) != 6 {
		t.Fatalf("calls = %v, want 6", calls)
	}
	if calls[0] != verifyCall {
		t.Errorf("first call = %q, want the password check", calls[0])
	}
	if calls[5] != deleteAuthCall {
		t.Errorf("last call = %q, want the login removed last", calls[5])
	}
	for _, name := range dataServices {
		if !slices.Contains(calls, name+" DELETE /internal/users/"+userA) {
			t.Errorf("%s was never told to delete: %v", name, calls)
		}
	}
}

func TestDeleteAccountPassesThePasswordOnlyToAuth(t *testing.T) {
	var seen string
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			raw, _ := io.ReadAll(r.Body)
			var in map[string]string
			_ = json.Unmarshal(raw, &in)
			seen = in["password"]
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer auth.Close()

	leaked := false
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), "secret-pass") {
			leaked = true
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer other.Close()

	g := newGatewayWith(t, upstreams{auth.URL, other.URL, other.URL, other.URL, other.URL})
	if w := postJSON(g, "/api/v1/account/delete", `{"password":"secret-pass"}`, tokenHeader(t)); w.Code != http.StatusNoContent {
		t.Fatalf("status = %d", w.Code)
	}
	if seen != "secret-pass" {
		t.Errorf("auth received password %q", seen)
	}
	if leaked {
		t.Error("the password was sent to a service other than auth")
	}
}

func TestDeleteAccountPassesAFirebaseSignInToAuth(t *testing.T) {
	var seen map[string]string
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &seen)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer auth.Close()
	other := accountService(t, "other", &callLog{}, http.StatusNoContent)

	g := newGatewayWith(t, upstreams{auth.URL, other, other, other, other})
	if w := postJSON(g, "/api/v1/account/delete", `{"firebase_id_token":"fresh"}`, tokenHeader(t)); w.Code != http.StatusNoContent {
		t.Fatalf("status = %d", w.Code)
	}
	if seen["firebase_id_token"] != "fresh" || seen["password"] != "" {
		t.Errorf("auth received %v", seen)
	}
}

func TestDeleteAccountWithAWrongPasswordDeletesNothing(t *testing.T) {
	log := &callLog{}
	u := allUp(t, log)
	u.auth = accountService(t, "auth", log, http.StatusUnauthorized)
	g := newGatewayWith(t, u)

	w := postJSON(g, "/api/v1/account/delete", `{"password":"wrong"}`, tokenHeader(t))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if decode(t, w)["code"] != "invalid_password" {
		t.Errorf("body = %s", w.Body.String())
	}
	if calls := log.all(); len(calls) != 1 || calls[0] != verifyCall {
		t.Errorf("only the password check may happen: %v", calls)
	}
}

func TestDeleteAccountKeepsTheLoginIfAServiceFails(t *testing.T) {
	log := &callLog{}
	u := allUp(t, log)
	u.activity = accountService(t, "activity", log, http.StatusInternalServerError)
	g := newGatewayWith(t, u)

	w := postJSON(g, "/api/v1/account/delete", `{"password":"secret-pass"}`, tokenHeader(t))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", w.Code)
	}
	body := decode(t, w)
	if body["code"] != "account_delete_incomplete" {
		t.Errorf("code = %v", body["code"])
	}
	if body["details"].(map[string]any)["activity"] == nil {
		t.Errorf("details must name the failed service: %v", body["details"])
	}
	if slices.Contains(log.all(), deleteAuthCall) {
		t.Error("the login must be kept so the person can sign in and try again")
	}
}

func TestDeleteAccountKeepsTheLoginIfAServiceIsDown(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()

	log := &callLog{}
	u := allUp(t, log)
	u.social = deadURL
	g := newGatewayWith(t, u)

	if w := postJSON(g, "/api/v1/account/delete", `{"password":"secret-pass"}`, tokenHeader(t)); w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", w.Code)
	}
	if slices.Contains(log.all(), deleteAuthCall) {
		t.Error("the login must be kept")
	}
}

func TestDeleteAccountWithAuthDownDeletesNothing(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()

	log := &callLog{}
	u := allUp(t, log)
	u.auth = deadURL
	g := newGatewayWith(t, u)

	if w := postJSON(g, "/api/v1/account/delete", `{"password":"x"}`, tokenHeader(t)); w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", w.Code)
	}
	if calls := log.all(); len(calls) != 0 {
		t.Errorf("without a password check nothing may be deleted: %v", calls)
	}
}

func TestDeleteAccountChecksTheRequest(t *testing.T) {
	log := &callLog{}
	g := newGatewayWith(t, allUp(t, log))

	if got := postJSON(g, "/api/v1/account/delete", `{"password":"x"}`, nil).Code; got != http.StatusUnauthorized {
		t.Errorf("no token: status = %d, want 401", got)
	}
	if got := postJSON(g, "/api/v1/account/delete", `{oops`, tokenHeader(t)).Code; got != http.StatusBadRequest {
		t.Errorf("bad JSON: status = %d, want 400", got)
	}
	if got := postJSON(g, "/api/v1/account/delete", `{}`, tokenHeader(t)).Code; got != http.StatusUnprocessableEntity {
		t.Errorf("no password: status = %d, want 422", got)
	}
	if calls := log.all(); len(calls) != 0 {
		t.Errorf("no service may be called: %v", calls)
	}
}
