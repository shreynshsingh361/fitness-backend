// Package clients is how this service asks other services for things it does not
// own: step totals (activity service) and names (auth service). It uses their
// /internal HTTP routes and never touches their databases.
package clients

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// nameBatch is how many names to ask for in one request.
const nameBatch = 500

type contextKey struct{}

// WithRequestID remembers the request id, so a call to another service can be
// followed through the logs along with the request that caused it.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}

func postJSON(ctx context.Context, client *http.Client, url string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if id, ok := ctx.Value(contextKey{}).(string); ok {
		req.Header.Set("X-Request-ID", id)
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("POST %s: unexpected status %d", url, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// ---------- activity service ----------

type Activity struct {
	base   string
	client *http.Client
}

func NewActivity(base string) *Activity {
	return &Activity{base: strings.TrimRight(base, "/"), client: &http.Client{Timeout: 10 * time.Second}}
}

type TotalsRequest struct {
	UserIDs      []string `json:"user_ids"`
	From         string   `json:"from"`
	To           string   `json:"to"`
	RevealBefore string   `json:"reveal_before,omitempty"`
}

type UserTotal struct {
	UserID        string `json:"user_id"`
	Steps         int    `json:"steps"`
	RevealedSteps int    `json:"revealed_steps"`
	DaysActive    int    `json:"days_active"`
	BestDay       int    `json:"best_day"`
	DaysGoalMet   int    `json:"days_goal_met"`
}

// Totals asks for people's step totals. People with no steps are not in the result.
func (a *Activity) Totals(ctx context.Context, req TotalsRequest) (map[string]UserTotal, error) {
	var out struct {
		Totals []UserTotal `json:"totals"`
	}
	if err := postJSON(ctx, a.client, a.base+"/internal/steps/totals", req, &out); err != nil {
		return nil, err
	}
	totals := make(map[string]UserTotal, len(out.Totals))
	for _, t := range out.Totals {
		totals[t.UserID] = t
	}
	return totals, nil
}

// ---------- auth service ----------

type Auth struct {
	base   string
	client *http.Client
}

func NewAuth(base string) *Auth {
	return &Auth{base: strings.TrimRight(base, "/"), client: &http.Client{Timeout: 5 * time.Second}}
}

// Names looks up display names by user id. Unknown ids are simply missing.
func (a *Auth) Names(ctx context.Context, ids []string) (map[string]string, error) {
	names := make(map[string]string, len(ids))
	for start := 0; start < len(ids); start += nameBatch {
		end := min(start+nameBatch, len(ids))
		var out struct {
			Users []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"users"`
		}
		in := map[string][]string{"ids": ids[start:end]}
		if err := postJSON(ctx, a.client, a.base+"/internal/users/lookup", in, &out); err != nil {
			return nil, err
		}
		for _, u := range out.Users {
			names[u.ID] = u.Name
		}
	}
	return names, nil
}
