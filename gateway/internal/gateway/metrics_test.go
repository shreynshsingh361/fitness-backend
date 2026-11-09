package gateway

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// After some traffic, /metrics shows requests by service and status class, a cumulative latency
// histogram, and how many requests were refused by a rate limit.
func TestMetricsCountRequestsLatencyAndRefusals(t *testing.T) {
	svc := fakeService(t)
	g := newGateway(t, svc.URL, svc.URL, svc.URL)
	hdr := bearer(t, userA)

	for range 3 {
		get(g, "GET", "/api/v1/steps/today", hdr)
	}
	get(g, "GET", "/api/v1/workouts", nil) // no token: 401, answered by the gateway itself
	for range 35 {                         // past the 30 a minute barcode limit
		get(g, "GET", "/api/v1/foods/barcode/5449000000996", hdr)
	}

	body := get(g, "GET", "/metrics", nil).Body.String()
	for _, want := range []string{
		`gateway_requests_total{service="activity",code="2xx"} 3`,
		`gateway_requests_total{service="gateway",code="4xx"} 6`, // the 401 and the five refused by the barcode limit
		`gateway_requests_total{service="nutrition",code="2xx"} 30`,
		`gateway_rate_limited_total 5`,
		`gateway_request_duration_seconds_count{service="activity"} 3`,
		`gateway_request_duration_seconds_bucket{service="activity",le="+Inf"} 3`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics is missing %q", want)
		}
	}
}

// Histogram buckets are cumulative: a request counts in its own bucket and in every larger one,
// and the +Inf bucket equals the total.
func TestHistogramBucketsAreCumulative(t *testing.T) {
	m := newMetrics()
	m.observe("auth", http.StatusOK, 5*time.Millisecond)   // under 0.01
	m.observe("auth", http.StatusOK, 300*time.Millisecond) // under 0.5
	m.observe("auth", http.StatusOK, 30*time.Second)       // over every bound
	var sb strings.Builder
	m.write(&sb)
	out := sb.String()
	for _, want := range []string{
		`le="0.01"} 1`, `le="0.25"} 1`, `le="0.5"} 2`, `le="10"} 2`, `le="+Inf"} 3`,
		`gateway_request_duration_seconds_count{service="auth"} 3`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
}
