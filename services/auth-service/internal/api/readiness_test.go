package api

import (
	"net/http"
	"testing"
)

// /ready must say "not ready" when there is no database to talk to, while /health (is the
// process alive) still says yes.
func TestReadinessNeedsTheDatabase(t *testing.T) {
	r := newTestRouter()
	if got := do(r, "GET", "/ready", "", nil).Code; got != http.StatusServiceUnavailable {
		t.Errorf("/ready without a database: status = %d, want 503", got)
	}
	if got := do(r, "GET", "/health", "", nil).Code; got != http.StatusOK {
		t.Errorf("/health: status = %d, want 200", got)
	}
}
