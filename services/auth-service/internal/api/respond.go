package api

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
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
