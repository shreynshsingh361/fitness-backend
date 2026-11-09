package gateway

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// latencyBuckets are the upper bounds (seconds) of the latency histogram. From them a monitor can
// work out the median and the p99 (what the slowest 1% of requests feel like).
var latencyBuckets = []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// metrics counts what the gateway does, in memory, and writes it in the Prometheus text format.
// It is small on purpose: requests by service and status class, how long they took, and how many
// were refused by a rate limit. It resets when the gateway restarts, which monitors expect.
type metrics struct {
	mu          sync.Mutex
	requests    map[string]uint64   // "service|2xx" -> count
	latency     map[string][]uint64 // service -> counts per bucket (+Inf last)
	latencySum  map[string]float64  // service -> total seconds
	rateLimited uint64
}

// newMetrics returns empty metrics.
func newMetrics() *metrics {
	return &metrics{requests: map[string]uint64{}, latency: map[string][]uint64{}, latencySum: map[string]float64{}}
}

// observe records one finished request: which service it went to, its status and its duration.
func (m *metrics) observe(service string, status int, took time.Duration) {
	class := fmt.Sprintf("%dxx", status/100)
	seconds := took.Seconds()

	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests[service+"|"+class]++
	if status == http.StatusTooManyRequests {
		m.rateLimited++
	}
	counts, ok := m.latency[service]
	if !ok {
		counts = make([]uint64, len(latencyBuckets)+1)
		m.latency[service] = counts
	}
	i := sort.SearchFloat64s(latencyBuckets, seconds) // the first bucket that holds it
	counts[i]++
	m.latencySum[service] += seconds
}

// middleware times every request and records it once it has finished.
func (m *metrics) middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		service := c.GetString("upstream")
		if service == "" {
			service = "gateway" // answered by the gateway itself, or no such route
		}
		m.observe(service, c.Writer.Status(), time.Since(start))
	}
}

// write prints the metrics as Prometheus text: counters, and a histogram whose buckets are
// cumulative (each counts everything at or under its bound), in a stable order.
func (m *metrics) write(sb *strings.Builder) {
	m.mu.Lock()
	defer m.mu.Unlock()

	sb.WriteString("# HELP gateway_requests_total Requests by service and status class.\n# TYPE gateway_requests_total counter\n")
	keys := make([]string, 0, len(m.requests))
	for k := range m.requests {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		service, class, _ := strings.Cut(k, "|")
		fmt.Fprintf(sb, "gateway_requests_total{service=%q,code=%q} %d\n", service, class, m.requests[k])
	}

	sb.WriteString("# HELP gateway_rate_limited_total Requests refused with 429.\n# TYPE gateway_rate_limited_total counter\n")
	fmt.Fprintf(sb, "gateway_rate_limited_total %d\n", m.rateLimited)

	sb.WriteString("# HELP gateway_request_duration_seconds Request duration by service.\n# TYPE gateway_request_duration_seconds histogram\n")
	services := make([]string, 0, len(m.latency))
	for s := range m.latency {
		services = append(services, s)
	}
	sort.Strings(services)
	for _, s := range services {
		var cumulative uint64
		for i, bound := range latencyBuckets {
			cumulative += m.latency[s][i]
			fmt.Fprintf(sb, "gateway_request_duration_seconds_bucket{service=%q,le=\"%g\"} %d\n", s, bound, cumulative)
		}
		cumulative += m.latency[s][len(latencyBuckets)]
		fmt.Fprintf(sb, "gateway_request_duration_seconds_bucket{service=%q,le=\"+Inf\"} %d\n", s, cumulative)
		fmt.Fprintf(sb, "gateway_request_duration_seconds_sum{service=%q} %g\n", s, m.latencySum[s])
		fmt.Fprintf(sb, "gateway_request_duration_seconds_count{service=%q} %d\n", s, cumulative)
	}
}

// handler serves GET /metrics. It is not reachable from the internet (the proxy in front only
// passes /api and /health); read it from the server itself.
func (m *metrics) handler(c *gin.Context) {
	var sb strings.Builder
	m.write(&sb)
	c.Data(http.StatusOK, "text/plain; version=0.0.4; charset=utf-8", []byte(sb.String()))
}
