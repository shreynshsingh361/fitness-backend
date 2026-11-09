package api

import (
	"log"
	"net/http"

	"context"
	"fitness/auth-service/internal/store"
	"github.com/gin-gonic/gin"
	"time"
)

// Every error from every service in this project has this same shape,
// so the frontend only has to understand one error format.
type errorBody struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Details map[string]string `json:"details,omitempty"`
}

func fail(c *gin.Context, status int, code, message string, details map[string]string) {
	c.AbortWithStatusJSON(status, errorBody{Code: code, Message: message, Details: details})
}

// serverError logs the real cause but tells the client nothing about it.
func serverError(c *gin.Context, err error) {
	log.Printf("req=%s internal error: %v", c.GetHeader("X-Request-ID"), err)
	fail(c, http.StatusInternalServerError, "internal_error", "something went wrong on our side", nil)
}

// readiness answers GET /ready: 200 only when the database answers within two seconds. /health
// says "the process is running"; this says "it can do its job", so Docker and the gateway stop
// counting on an instance whose database is gone.
func readiness(service string, s *store.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		if s == nil || s.Ping(ctx) != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable", "service": service})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ready", "service": service})
	}
}
