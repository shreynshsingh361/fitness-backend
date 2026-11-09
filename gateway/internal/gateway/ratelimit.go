package gateway

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// limiter allows each client (by IP) a number of requests per window. It guards
// sign-in, sign-up and account deletion, so nobody can try thousands of passwords.
//
// It keeps counts in memory, so with several gateway copies each one counts on its
// own. That is fine for one server.
type limiter struct {
	max    int
	window time.Duration
	now    func() time.Time

	mu      sync.Mutex
	clients map[string]*window
	swept   time.Time
}

type window struct {
	start time.Time
	count int
}

func newLimiter(max int, per time.Duration) *limiter {
	return &limiter{max: max, window: per, now: time.Now, clients: map[string]*window{}}
}

// allow counts one request from key and says whether it is within the limit, and
// if not, how long until the next one is allowed.
func (l *limiter) allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()

	// Forget clients whose window ended, now and then, so the map does not grow forever.
	if now.Sub(l.swept) >= l.window {
		for k, w := range l.clients {
			if now.Sub(w.start) >= l.window {
				delete(l.clients, k)
			}
		}
		l.swept = now
	}

	w, ok := l.clients[key]
	if !ok || now.Sub(w.start) >= l.window {
		l.clients[key] = &window{start: now, count: 1}
		return true, 0
	}
	if w.count >= l.max {
		return false, w.start.Add(l.window).Sub(now)
	}
	w.count++
	return true, 0
}

// limit counts one request from key (an IP address, or a user id) and answers 429 with a
// Retry-After header once that key has used up its requests. It returns false after answering.
func (l *limiter) limit(c *gin.Context, key string) bool {
	ok, wait := l.allow(key)
	if ok {
		return true
	}
	seconds := int(wait.Round(time.Second) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	c.Header("Retry-After", strconv.Itoa(seconds))
	fail(c, http.StatusTooManyRequests, "too_many_attempts",
		"too many attempts; wait "+strconv.Itoa(seconds)+" seconds and try again", nil)
	return false
}
