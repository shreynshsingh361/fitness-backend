package clients

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// instant is a caller that does not really wait and has no random pause, with a clock the test moves.
func instant(clock *time.Time) (*caller, *[]time.Duration) {
	var pauses []time.Duration
	return &caller{
		breaker: newBreaker(func() time.Time { return *clock }),
		sleep:   func(_ context.Context, d time.Duration) error { pauses = append(pauses, d); return nil },
		jitter:  func() float64 { return 1 }, // the longest pause, so the growth shows
	}, &pauses
}

// A service that fails for a moment (503) and then recovers is retried, with pauses that double.
func TestRetriesTemporaryFailuresWithGrowingPauses(t *testing.T) {
	now := time.Now()
	c, pauses := instant(&now)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	var out map[string]any
	if err := postJSON(context.Background(), c, srv.Client(), srv.URL, map[string]int{}, &out); err != nil {
		t.Fatalf("a service that recovers by the third try must succeed: %v", err)
	}
	if calls.Load() != 3 {
		t.Errorf("calls = %d, want 3", calls.Load())
	}
	if len(*pauses) != 2 || (*pauses)[0] != backoffBase || (*pauses)[1] != 2*backoffBase {
		t.Errorf("pauses = %v, want %v then %v", *pauses, backoffBase, 2*backoffBase)
	}
}

// A refusal that would come out the same again (400, 404, 422) is not retried.
func TestDoesNotRetryAnswersThatWouldRepeat(t *testing.T) {
	now := time.Now()
	c, pauses := instant(&now)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnprocessableEntity)
	}))
	defer srv.Close()

	if err := postJSON(context.Background(), c, srv.Client(), srv.URL, map[string]int{}, new(map[string]any)); err == nil {
		t.Fatal("a 422 must be an error")
	}
	if calls.Load() != 1 || len(*pauses) != 0 {
		t.Errorf("calls = %d, pauses = %v; want one call and no waiting", calls.Load(), *pauses)
	}
}

// After five failures in a row the service rests: calls fail at once, without touching it. After
// the cooldown one trial call is allowed, and a success closes the breaker again.
func TestBreakerOpensRestsAndRecovers(t *testing.T) {
	now := time.Now()
	c, _ := instant(&now)
	var calls atomic.Int32
	healthy := atomic.Bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if healthy.Load() {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError) // not retried, so one call = one failure
	}))
	defer srv.Close()
	call := func() error {
		return postJSON(context.Background(), c, srv.Client(), srv.URL, map[string]int{}, new(map[string]any))
	}

	for range breakerThreshold {
		if err := call(); err == nil || errors.Is(err, ErrCircuitOpen) {
			t.Fatalf("a failing call before the breaker opens must fail on its own: %v", err)
		}
	}
	reached := calls.Load()
	if err := call(); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("with the breaker open: err = %v, want ErrCircuitOpen", err)
	}
	if calls.Load() != reached {
		t.Fatal("an open breaker must not call the service")
	}

	now = now.Add(breakerCooldown + time.Second) // the rest is over
	healthy.Store(true)
	if err := call(); err != nil {
		t.Fatalf("the trial call to a recovered service must work: %v", err)
	}
	if err := call(); err != nil {
		t.Fatalf("the breaker must be closed again: %v", err)
	}
}

// While a trial call is in flight, other calls are still refused (one trial at a time).
func TestOnlyOneTrialAfterTheCooldown(t *testing.T) {
	now := time.Now()
	b := newBreaker(func() time.Time { return now })
	for range breakerThreshold {
		b.record(true)
	}
	if b.allow() {
		t.Fatal("open: must refuse")
	}
	now = now.Add(breakerCooldown + time.Second)
	if !b.allow() {
		t.Fatal("after the cooldown the first call is the trial")
	}
	if b.allow() {
		t.Fatal("a second call while the trial is running must be refused")
	}
	b.record(true) // the trial failed: the rest starts again
	if b.allow() {
		t.Fatal("after a failed trial the breaker must be open again")
	}
}

// A cancelled request stops the retrying and is not counted against the service.
func TestCancelledRequestStopsRetriesAndIsNotBlamedOnTheService(t *testing.T) {
	now := time.Now()
	c, _ := instant(&now)
	ctx, cancel := context.WithCancel(context.Background())
	c.sleep = func(ctx context.Context, d time.Duration) error { cancel(); return ctx.Err() }
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	err := postJSON(ctx, c, srv.Client(), srv.URL, map[string]int{}, new(map[string]any))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want 1 (no retry after cancelling)", calls.Load())
	}
}

// retryable sorts failures: connection trouble and 502/503/504 are worth another try; the rest is not.
func TestRetryable(t *testing.T) {
	cases := map[string]struct {
		err  error
		want bool
	}{
		"503":             {statusError{503}, true},
		"502":             {statusError{502}, true},
		"500":             {statusError{500}, false},
		"404":             {statusError{404}, false},
		"cancelled":       {context.Canceled, false},
		"deadline":        {context.DeadlineExceeded, false},
		"connection lost": {errors.New("connection reset by peer"), true},
	}
	for name, tc := range cases {
		if got := retryable(tc.err); got != tc.want {
			t.Errorf("%s: retryable = %v, want %v", name, got, tc.want)
		}
	}
}
