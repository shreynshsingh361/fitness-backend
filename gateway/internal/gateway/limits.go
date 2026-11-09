package gateway

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// Limits that keep one slow, buggy or hostile client from using up the server.
const (
	// maxBodyBytes is the largest request body the API takes. The biggest real request is a
	// 60-day step sync (a few KB), so 1 MiB is far above any honest use.
	maxBodyBytes = 1 << 20

	// userRequestsPerMinute is the overall ceiling for one signed-in user. The app, even syncing
	// steps every second, stays well under 10 requests a second.
	userRequestsPerMinute = 600

	// barcodeLookupsPerMinute limits the barcode lookup, which can make the server call Open
	// Food Facts (up to 5 seconds each). Someone scanning shelves does a few a minute.
	barcodeLookupsPerMinute = 30
	barcodePrefix           = "/api/v1/foods/barcode/"

	// Timeouts of the HTTP server. They stop a client that sends a byte a minute, or never reads
	// the answer, from holding a connection for ever. WriteTimeout must be longer than the
	// slowest answer: deleting an account waits up to 20 seconds for the services.
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 15 * time.Second
	writeTimeout      = 30 * time.Second
	idleTimeout       = 60 * time.Second
	maxHeaderBytes    = 16 << 10
)

// NewServer returns an HTTP server for handler with every timeout and size limit set. Use it
// instead of a bare http.Server, which waits for ever by default.
func NewServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
	}
}

// bodyLimit refuses a request whose body is over maxBodyBytes with 413, before any of it is
// read. A body that does not say its size up front (chunked) is cut off at the limit as it
// is read, so it can never fill the server's memory either way.
func bodyLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.ContentLength > maxBodyBytes {
			fail(c, http.StatusRequestEntityTooLarge, "payload_too_large",
				"the request body is too large", nil)
			c.Abort()
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBodyBytes)
		c.Next()
	}
}

// limitUser counts one request from a signed-in user against their limits and answers 429 when
// they are over: the overall ceiling for everything, and a tighter one for the barcode lookup.
// It returns false once it has answered. Each user is counted on their own.
func (g *gateway) limitUser(c *gin.Context, userID string) bool {
	if strings.HasPrefix(c.Request.URL.Path, barcodePrefix) && !g.barcodes.limit(c, userID) {
		return false
	}
	return g.perUser.limit(c, userID)
}
