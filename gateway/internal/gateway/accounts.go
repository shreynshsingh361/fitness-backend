package gateway

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// How long the gateway trusts what it learned about an account.
const (
	// accountKnownTTL: an account that exists is not asked about again for a minute, so the check
	// costs one small call per active user per minute, not one per request.
	accountKnownTTL = time.Minute

	// accountGoneTTL: a deleted account stays refused for longer than an access token lives (15
	// minutes), so none of the tokens issued before the deletion can work again.
	accountGoneTTL = 20 * time.Minute

	// accountCheckTimeout is how long to wait for the auth service before giving up and letting
	// the request through (see check).
	accountCheckTimeout = 2 * time.Second
)

// errAccountGone is what authenticate returns for a valid token whose account was deleted.
var errAccountGone = errors.New("the account no longer exists")

// verdict is what the gateway last learned about one account, and until when to believe it.
type verdict struct {
	exists bool
	until  time.Time
}

// accountChecker answers "does this account still exist?" for tokens that are otherwise valid. A
// token is signed once and stays good for 15 minutes, so on its own it cannot tell that the person
// deleted their account a minute ago. The services keep no link to the users table, so a request
// carrying such a token would quietly recreate data that nobody would ever clean up.
type accountChecker struct {
	authURL string
	client  *http.Client
	now     func() time.Time // replaced in tests

	mu        sync.Mutex
	known     map[string]verdict
	lastSweep time.Time
}

// newAccountChecker returns a checker that asks the auth service at authURL.
func newAccountChecker(authURL string, client *http.Client) *accountChecker {
	return &accountChecker{authURL: authURL, client: client, now: time.Now, known: map[string]verdict{}}
}

// check says whether the account exists. A recent answer is reused. Otherwise the auth service is
// asked: 404 means deleted, a 2xx means it exists. If the auth service cannot be asked, check
// returns an error and the caller lets the request through (and does not remember anything): with
// auth down, people who are signed in keep working, which matters more than a deleted account's
// token working for a few more minutes.
func (a *accountChecker) check(ctx context.Context, userID, requestID string) (bool, error) {
	if v, ok := a.cached(userID); ok {
		return v, nil
	}
	ctx, cancel := context.WithTimeout(ctx, accountCheckTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.authURL+"/internal/users/"+userID+"/exists", nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("X-Request-ID", requestID)
	resp, err := a.client.Do(req)
	if err != nil {
		return false, err
	}
	resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		a.remember(userID, false)
		return false, nil
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		a.remember(userID, true)
		return true, nil
	}
	return false, errors.New("auth service answered " + http.StatusText(resp.StatusCode))
}

// cached returns the remembered answer for userID, if there is one that has not run out.
func (a *accountChecker) cached(userID string) (exists, ok bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	v, found := a.known[userID]
	if !found || !a.now().Before(v.until) {
		return false, false
	}
	return v.exists, true
}

// remember stores what was learned about userID. Expired entries are swept out at most once a
// minute, so the map holds only people seen recently, however long the gateway runs.
func (a *accountChecker) remember(userID string, exists bool) {
	ttl := accountGoneTTL
	if exists {
		ttl = accountKnownTTL
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	a.known[userID] = verdict{exists: exists, until: now.Add(ttl)}
	if now.Sub(a.lastSweep) >= time.Minute {
		for id, v := range a.known {
			if !now.Before(v.until) {
				delete(a.known, id)
			}
		}
		a.lastSweep = now
	}
}

// markDeleted refuses userID from now on, without asking the auth service. Account deletion calls
// it before it starts removing data, so that no request can add new data behind its back.
func (a *accountChecker) markDeleted(userID string) { a.remember(userID, false) }

// unmark forgets what is known about userID. Account deletion calls it when it fails part-way and
// the login is kept, so the person is not locked out of an account that still exists.
func (a *accountChecker) unmark(userID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.known, userID)
}

// warnUnchecked logs that a request went through without the account check.
func warnUnchecked(requestID string, err error) {
	slog.Warn("could not check that the account exists; letting the request through", "req", requestID, "err", err.Error())
}
