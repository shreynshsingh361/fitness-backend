package api

import (
	"log"
	"net/http"
	"strconv"

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

// page is the envelope used by every list endpoint.
type page[T any] struct {
	Items []T `json:"items"`
	Page  int `json:"page"`
	Limit int `json:"limit"`
	Total int `json:"total"`
}

// pageParams reads ?page=&limit= (defaults 1 and 20, limit capped at 100).
func pageParams(c *gin.Context) (pageNum, limit, offset int) {
	pageNum = intQuery(c, "page", 1)
	limit = intQuery(c, "limit", 20)
	if pageNum < 1 {
		pageNum = 1
	}
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	return pageNum, limit, (pageNum - 1) * limit
}

func intQuery(c *gin.Context, key string, fallback int) int {
	if v, err := strconv.Atoi(c.Query(key)); err == nil {
		return v
	}
	return fallback
}
