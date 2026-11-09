package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// dataServices hold a person's data. The auth service is not here because it is
// always handled separately and last (see deleteAccount).
var dataServices = []string{"workout", "nutrition", "activity", "social"}

// deleteAccount permanently removes a person from the whole system. No single
// service can do this, because each one only knows about its own data and there is
// no database transaction across services. The order is what makes it safe:
//
//  1. Check the password. Nothing has happened yet if it is wrong.
//  2. Delete the person's data from every other service. Each delete can be repeated.
//  3. Delete the login LAST. If a service was down in step 2 we stop here, so the
//     person can still sign in and simply try again.
func (g *gateway) deleteAccount(c *gin.Context) {
	userID, err := g.authenticate(c.Request)
	if err != nil {
		c.Header("WWW-Authenticate", `Bearer realm="fitness"`)
		fail(c, http.StatusUnauthorized, "unauthorized", "missing, invalid or expired access token", nil)
		return
	}
	if !g.attempts.limit(c) {
		return
	}
	// The password, or for an account that signs in with Firebase, a fresh Firebase sign-in.
	var in struct {
		Password        string `json:"password"`
		FirebaseIDToken string `json:"firebase_id_token"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "invalid_json", "request body must be valid JSON", nil)
		return
	}
	if in.Password == "" && in.FirebaseIDToken == "" {
		fail(c, http.StatusUnprocessableEntity, "validation_error", "some fields are invalid",
			map[string]string{"password": "is required (or firebase_id_token)"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	requestID := c.Request.Header.Get("X-Request-ID")

	// 1. The password. A login's secret never leaves the auth service: it only says yes or no.
	body, err := json.Marshal(map[string]string{"password": in.Password, "firebase_id_token": in.FirebaseIDToken})
	if err != nil {
		fail(c, http.StatusInternalServerError, "internal_error", "something went wrong on our side", nil)
		return
	}
	status, err := g.send(ctx, http.MethodPost,
		g.services["auth"]+"/internal/users/"+userID+"/verify-password", body, requestID)
	switch {
	case err != nil:
		log.Printf("req=%s account delete: auth unavailable: %v", requestID, err)
		fail(c, http.StatusBadGateway, "account_delete_incomplete", "nothing was deleted; please try again", nil)
		return
	case status == http.StatusUnauthorized || status == http.StatusNotFound:
		fail(c, http.StatusUnauthorized, "invalid_password", "the password is incorrect", nil)
		return
	case status != http.StatusNoContent:
		log.Printf("req=%s account delete: auth answered %d", requestID, status)
		fail(c, http.StatusBadGateway, "account_delete_incomplete", "nothing was deleted; please try again", nil)
		return
	}

	// 2. The data, in every service at the same time.
	var mu sync.Mutex
	var wg sync.WaitGroup
	failed := map[string]string{}
	for _, name := range dataServices {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, err := g.send(ctx, http.MethodDelete, g.services[name]+"/internal/users/"+userID, nil, requestID)
			if err == nil && status != http.StatusNoContent {
				err = fmt.Errorf("unexpected status %d", status)
			}
			if err != nil {
				log.Printf("req=%s account delete: %s failed: %v", requestID, name, err)
				mu.Lock()
				failed[name] = "did not answer"
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(failed) > 0 {
		names := make([]string, 0, len(failed))
		for name := range failed {
			names = append(names, name)
		}
		sort.Strings(names)
		log.Printf("req=%s account delete stopped; login kept; failed: %v", requestID, names)
		fail(c, http.StatusBadGateway, "account_delete_incomplete",
			"some of your data could not be deleted yet; your login still works, please try again", failed)
		return
	}

	// 3. The login, last.
	status, err = g.send(ctx, http.MethodDelete, g.services["auth"]+"/internal/users/"+userID, nil, requestID)
	if err != nil || status != http.StatusNoContent {
		log.Printf("req=%s account delete: removing the login failed: status=%d err=%v", requestID, status, err)
		fail(c, http.StatusBadGateway, "account_delete_incomplete",
			"your data was deleted but your login could not be; please try again", nil)
		return
	}
	c.Status(http.StatusNoContent)
}

// send makes one call to another service and reports the status it answered with.
func (g *gateway) send(ctx context.Context, method, target string, body []byte, requestID string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Request-ID", requestID)

	resp, err := g.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}
