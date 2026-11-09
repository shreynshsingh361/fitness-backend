package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// requestLog writes one structured log line per request (not for the health checks, which Docker
// repeats every few seconds): the request id (it follows the request through every service),
// the user, the route, the status and how long it took. A 5xx is logged at error level.
func requestLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		path := c.Request.URL.Path
		if path == "/health" || path == "/ready" {
			return
		}
		level := slog.LevelInfo
		if c.Writer.Status() >= http.StatusInternalServerError {
			level = slog.LevelError
		}
		slog.Log(c.Request.Context(), level, "request",
			"req", c.GetHeader("X-Request-ID"), "user", c.GetHeader("X-User-ID"),
			"method", c.Request.Method, "path", path, "status", c.Writer.Status(),
			"ms", time.Since(start).Milliseconds())
	}
}

// requireUser trusts the X-User-ID header. That is safe ONLY because this
// service is not reachable from the internet: the gateway verifies the JWT,
// then sets this header itself (replacing anything the client sent).
func requireUser() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader("X-User-ID")
		if _, err := uuid.Parse(id); err != nil {
			fail(c, http.StatusUnauthorized, "unauthorized", "missing or invalid user", nil)
			return
		}
		c.Set("userID", id)
		c.Next()
	}
}

func userID(c *gin.Context) string { return c.GetString("userID") }
