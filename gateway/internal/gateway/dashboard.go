package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
)

// dashboard answers ONE request from the browser by asking several services at the
// same time and combining their answers. This is why a gateway is useful: the
// frontend makes one call instead of several.
//
// If a service is down, the dashboard still works: that part is null and its
// name is listed in "unavailable".
func (g *gateway) dashboard(c *gin.Context) {
	userID, err := g.authenticate(c.Request)
	if err != nil {
		c.Header("WWW-Authenticate", `Bearer realm="fitness"`)
		fail(c, http.StatusUnauthorized, "unauthorized", "missing, invalid or expired access token", nil)
		return
	}

	date := c.Query("date") // the browser sends its own local "today"
	if date == "" {
		date = time.Now().UTC().Format("2006-01-02")
	} else if _, err := time.Parse("2006-01-02", date); err != nil {
		fail(c, http.StatusUnprocessableEntity, "validation_error", "some fields are invalid",
			map[string]string{"date": "must look like 2026-11-07"})
		return
	}

	calls := []struct{ name, url string }{
		{"workouts", g.services["workout"] + "/api/v1/workouts/summary?days=7"},
		{"nutrition", g.services["nutrition"] + "/api/v1/nutrition/summary?date=" + url.QueryEscape(date)},
		{"steps", g.services["activity"] + "/api/v1/steps/today?date=" + url.QueryEscape(date)},
	}
	type result struct {
		name string
		body json.RawMessage
		err  error
	}
	results := make(chan result, len(calls))

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	requestID := c.Request.Header.Get("X-Request-ID")
	for _, call := range calls {
		go func() { // these run at the same time
			body, err := g.getJSON(ctx, call.url, userID, requestID)
			results <- result{call.name, body, err}
		}()
	}

	response := gin.H{"date": date, "workouts": nil, "nutrition": nil, "steps": nil}
	unavailable := []string{}
	for range calls {
		r := <-results
		if r.err != nil {
			log.Printf("req=%s dashboard: %s unavailable: %v", requestID, r.name, r.err)
			unavailable = append(unavailable, r.name)
			continue
		}
		response[r.name] = r.body
	}
	sort.Strings(unavailable)
	response["unavailable"] = unavailable
	c.JSON(http.StatusOK, response)
}

func (g *gateway) getJSON(ctx context.Context, target, userID, requestID string) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-User-ID", userID)
	req.Header.Set("X-Request-ID", requestID)

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

// probeServices asks every service for path (/health or /ready) in parallel, with a shared three
// second deadline. It returns each service's "up" or "down" and whether all of them are up.
func (g *gateway) probeServices(ctx context.Context, path string) (services gin.H, allUp bool) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	type check struct {
		name string
		up   bool
	}
	results := make(chan check, len(g.services))
	for name, base := range g.services {
		go func() {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
			if err != nil {
				results <- check{name, false}
				return
			}
			resp, err := g.client.Do(req)
			if err != nil {
				results <- check{name, false}
				return
			}
			resp.Body.Close()
			results <- check{name, resp.StatusCode == http.StatusOK}
		}()
	}

	services, allUp = gin.H{"gateway": "up"}, true
	for range g.services {
		r := <-results
		if r.up {
			services[r.name] = "up"
		} else {
			services[r.name] = "down"
			allUp = false
		}
	}
	return services, allUp
}

// serviceHealth shows which services are alive. Handy when something does not work: it names the
// container that is the problem. It always answers 200; use /ready to act on the result.
func (g *gateway) serviceHealth(c *gin.Context) {
	services, allUp := g.probeServices(c.Request.Context(), "/health")
	status := "ok"
	if !allUp {
		status = "degraded"
	}
	c.JSON(http.StatusOK, gin.H{"status": status, "services": services})
}

// ready answers 200 only when every service is ready (alive and able to reach its database),
// else 503. A monitor or deploy script polls this to know the whole system works.
func (g *gateway) ready(c *gin.Context) {
	services, allUp := g.probeServices(c.Request.Context(), "/ready")
	if !allUp {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable", "services": services})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ready", "services": services})
}
