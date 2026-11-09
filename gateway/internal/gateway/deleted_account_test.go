package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// authSaying is a fake auth service that answers the "does this account exist?" question with
// status for every user, and counts how many times it was asked.
func authSaying(t *testing.T, status int) (url string, asked *int) {
	t.Helper()
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &n
}

// A token is signed once and lives 15 minutes, so after someone deletes their account (say, from
// another phone) their old token is still a valid signature. The gateway must ask whether the
// account exists, or a request with that token would recreate data nobody can ever clean up.
func TestTokenOfADeletedAccountIsRefused(t *testing.T) {
	other := fakeService(t)
	gone, _ := authSaying(t, http.StatusNotFound)
	g := newGatewayWith(t, upstreams{auth: gone, workout: other.URL, nutrition: other.URL, activity: other.URL, social: other.URL})

	w := postJSON(g, "/api/v1/diary", `{"food_id":"x"}`, tokenHeader(t))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("deleted account's token: status = %d, want 401", w.Code)
	}
	if decode(t, w)["code"] != "unauthorized" {
		t.Errorf("the code must be the one the app already treats as 'sign in again': %s", w.Body.String())
	}

	alive, _ := authSaying(t, http.StatusNoContent)
	g = newGatewayWith(t, upstreams{auth: alive, workout: other.URL, nutrition: other.URL, activity: other.URL, social: other.URL})
	if got := postJSON(g, "/api/v1/diary", `{"food_id":"x"}`, tokenHeader(t)).Code; got != http.StatusOK {
		t.Errorf("an account that exists: status = %d, want 200", got)
	}
}

// The check costs one small call per person per minute, not one per request.
func TestAccountCheckIsRememberedForAMinute(t *testing.T) {
	other := fakeService(t)
	alive, asked := authSaying(t, http.StatusNoContent)
	g := newGatewayWith(t, upstreams{auth: alive, workout: other.URL, nutrition: other.URL, activity: other.URL, social: other.URL})
	for range 20 {
		get(g, "GET", "/api/v1/steps/today", tokenHeader(t))
	}
	if *asked != 1 {
		t.Fatalf("auth was asked %d times for 20 requests, want 1", *asked)
	}
}

// If the auth service cannot be asked, people who are signed in keep working; and nothing wrong
// is remembered, so the next request asks again.
func TestAccountCheckFailsOpenWhenAuthIsDown(t *testing.T) {
	other := fakeService(t)
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close() // nothing is listening any more
	g := newGatewayWith(t, upstreams{auth: down.URL, workout: other.URL, nutrition: other.URL, activity: other.URL, social: other.URL})
	for range 2 {
		if got := get(g, "GET", "/api/v1/steps/today", tokenHeader(t)).Code; got != http.StatusOK {
			t.Fatalf("auth unreachable: status = %d, want 200 (fail open)", got)
		}
	}
}

// With a clock the test moves: an account that exists is asked about again after a minute, and a
// deleted one stays refused for longer than an access token lives.
func TestAccountCheckerRemembersForTheRightTime(t *testing.T) {
	status := http.StatusNoContent
	asked := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked++
		w.WriteHeader(status)
	}))
	defer srv.Close()
	now := time.Now()
	a := newAccountChecker(srv.URL, srv.Client())
	a.now = func() time.Time { return now }
	ctx := context.Background()

	if ok, err := a.check(ctx, userA, "r"); err != nil || !ok {
		t.Fatalf("first check: ok=%v err=%v", ok, err)
	}
	now = now.Add(accountKnownTTL - time.Second)
	_, _ = a.check(ctx, userA, "r")
	if asked != 1 {
		t.Fatalf("within the minute: asked %d times, want 1", asked)
	}
	now = now.Add(2 * time.Second)
	status = http.StatusNotFound // deleted meanwhile
	if ok, _ := a.check(ctx, userA, "r"); ok || asked != 2 {
		t.Fatalf("after the minute it must ask again and learn it is gone: ok=%v asked=%d", ok, asked)
	}
	now = now.Add(15 * time.Minute) // as long as an access token lives
	status = http.StatusNoContent   // even if auth said otherwise now, the verdict stands
	if ok, _ := a.check(ctx, userA, "r"); ok {
		t.Fatal("a deleted account must stay refused for the whole life of its old tokens")
	}
	now = now.Add(6 * time.Minute)
	if ok, _ := a.check(ctx, userA, "r"); !ok {
		t.Fatal("after the long memory runs out it asks auth again")
	}
}

// Deleting an account: from the moment it starts, nothing may be written for that account (a
// second phone, an offline queue sending late), because the data is being removed. If it
// finishes the account stays refused; if it fails part-way the login is kept and works again.
func TestAccountIsRefusedWhileItIsBeingDeleted(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-release // the data service takes its time
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(slow.Close)
	log := &callLog{}
	up := allUp(t, log)
	up.workout = slow.URL
	g := newGatewayWith(t, up)

	done := make(chan int, 1)
	go func() {
		done <- postJSON(g, "/api/v1/account/delete", `{"password":"secret-pass"}`, tokenHeader(t)).Code
	}()
	<-started // the data is being removed right now

	if got := postJSON(g, "/api/v1/diary", `{"food_id":"x"}`, tokenHeader(t)).Code; got != http.StatusUnauthorized {
		t.Errorf("a write during deletion: status = %d, want 401", got)
	}
	close(release)
	if code := <-done; code != http.StatusNoContent {
		t.Fatalf("deletion: status = %d, want 204", code)
	}
	if got := postJSON(g, "/api/v1/diary", `{"food_id":"x"}`, tokenHeader(t)).Code; got != http.StatusUnauthorized {
		t.Errorf("after deletion the old token must stay refused: status = %d, want 401", got)
	}
}

// A deletion that fails part-way keeps the login, so the account must not stay locked.
func TestAccountWorksAgainIfDeletionFailsPartWay(t *testing.T) {
	log := &callLog{}
	up := allUp(t, log)
	up.social = accountService(t, "social", log, http.StatusInternalServerError)
	g := newGatewayWith(t, up)

	if got := postJSON(g, "/api/v1/account/delete", `{"password":"secret-pass"}`, tokenHeader(t)).Code; got != http.StatusBadGateway {
		t.Fatalf("a failing service: status = %d, want 502", got)
	}
	if got := postJSON(g, "/api/v1/diary", `{"food_id":"x"}`, tokenHeader(t)).Code; got == http.StatusUnauthorized {
		t.Error("after a failed deletion the account still exists and must not be locked out")
	}
}
