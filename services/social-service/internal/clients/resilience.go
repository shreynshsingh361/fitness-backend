package clients

import (
	"context"
	"errors"
	"math/rand/v2"
	"net"
	"sync"
	"time"
)

// Settings for calls to other services. A call that fails fast (connection refused, a 502/503/504)
// is tried again after a short, growing, randomised pause. After breakerThreshold failures in a
// row the service is given breakerCooldown of rest, during which calls fail at once instead of each
// waiting for a timeout: a sick service then does not drag this one down with it.
const (
	maxAttempts      = 3
	backoffBase      = 100 * time.Millisecond
	breakerThreshold = 5
	breakerCooldown  = 10 * time.Second
)

// ErrCircuitOpen is returned without calling the service while it is resting after repeated failures.
var ErrCircuitOpen = errors.New("service is failing: calls are paused for a moment")

// statusError is a service answering with a status other than 200.
type statusError struct{ code int }

// Error describes the unexpected status.
func (e statusError) Error() string { return "unexpected status " + itoa(e.code) }

// retryable says whether trying again could help: a failure to connect, or the service itself
// being unavailable (502, 503, 504). A timeout is not retried (it already cost the full wait, and
// trying again would only repeat it), nor is a 4xx, which would answer the same way again.
func retryable(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var status statusError
	if errors.As(err, &status) {
		return status.code == 502 || status.code == 503 || status.code == 504
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return false
	}
	return true // the connection itself failed
}

// breaker is a circuit breaker for one other service. Closed: calls go through. Open: calls fail
// at once until the cooldown is over. Then one trial call is let through (half-open): if it works
// the breaker closes, if it fails the cooldown starts again.
type breaker struct {
	mu        sync.Mutex
	failures  int       // failures in a row
	openUntil time.Time // while in the future, calls are refused
	trial     bool      // a trial call after the cooldown is in flight
	now       func() time.Time
}

// newBreaker returns a closed breaker that reads the time from now.
func newBreaker(now func() time.Time) *breaker { return &breaker{now: now} }

// allow says whether a call may go ahead. While the breaker is open it says no; once the cooldown
// is over it lets exactly one trial call through and says no to the others until that one is done.
func (b *breaker) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failures < breakerThreshold {
		return true
	}
	if b.now().Before(b.openUntil) || b.trial {
		return false
	}
	b.trial = true
	return true
}

// record notes how a call ended: a success closes the breaker, a failure counts towards opening it
// (and, for a failed trial, starts the cooldown again).
func (b *breaker) record(failed bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.trial = false
	if !failed {
		b.failures = 0
		return
	}
	b.failures++
	if b.failures >= breakerThreshold {
		b.openUntil = b.now().Add(breakerCooldown)
	}
}

// caller makes calls to one other service with retries and a circuit breaker.
type caller struct {
	breaker *breaker
	sleep   func(ctx context.Context, d time.Duration) error // replaced in tests so they do not wait
	jitter  func() float64                                   // 0 up to 1; replaced in tests
}

// newCaller returns a caller that really waits between attempts.
func newCaller() *caller {
	return &caller{breaker: newBreaker(time.Now), sleep: sleepContext, jitter: rand.Float64}
}

// sleepContext waits for d, or returns early with the context's error if it is cancelled first.
func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// do runs call, trying again after a randomised pause (full jitter: anywhere from nothing up to
// 100 ms, then 200 ms) when a retry could help, at most maxAttempts times. It refuses with
// ErrCircuitOpen while the breaker is open. Only reads may go through here: a call that changes
// something must not be repeated.
func (c *caller) do(ctx context.Context, call func() error) error {
	if !c.breaker.allow() {
		return ErrCircuitOpen
	}
	var err error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err = call(); err == nil {
			c.breaker.record(false)
			return nil
		}
		if !retryable(err) || attempt == maxAttempts-1 {
			break
		}
		pause := time.Duration(c.jitter() * float64(backoffBase<<attempt))
		if sleepErr := c.sleep(ctx, pause); sleepErr != nil {
			err = sleepErr
			break
		}
	}
	// A 4xx or a cancelled request says nothing about the service's health.
	var status statusError
	healthy := (errors.As(err, &status) && status.code < 500) || errors.Is(err, context.Canceled)
	c.breaker.record(!healthy)
	return err
}
