package api

import (
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// requestLog prints one line per request. The request id is created by the
// gateway, so you can follow a single click through every service's logs.
func requestLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		log.Printf("req=%s user=%s %s %s -> %d (%s)",
			c.GetHeader("X-Request-ID"), c.GetHeader("X-User-ID"),
			c.Request.Method, c.Request.URL.Path,
			c.Writer.Status(), time.Since(start).Round(time.Millisecond))
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
