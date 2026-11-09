package clients

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTotalsSendsTheRequestAndReadsTheAnswer(t *testing.T) {
	var got TotalsRequest
	var requestID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/steps/totals" || r.Method != http.MethodPost {
			t.Errorf("called %s %s", r.Method, r.URL.Path)
		}
		requestID = r.Header.Get("X-Request-ID")
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"totals":[{"user_id":"a","steps":900,"revealed_steps":400,"days_active":2,"best_day":500,"days_goal_met":1}]}`))
	}))
	defer srv.Close()

	ctx := WithRequestID(context.Background(), "req-1")
	totals, err := NewActivity(srv.URL+"/").Totals(ctx, TotalsRequest{
		UserIDs: []string{"a", "b"}, From: "2026-11-01", To: "2026-11-07", RevealBefore: "2026-11-07",
	})
	if err != nil {
		t.Fatal(err)
	}
	if requestID != "req-1" {
		t.Errorf("X-Request-ID = %q, want it passed along", requestID)
	}
	if len(got.UserIDs) != 2 || got.From != "2026-11-01" || got.RevealBefore != "2026-11-07" {
		t.Errorf("sent %+v", got)
	}
	if a := totals["a"]; a.Steps != 900 || a.RevealedSteps != 400 || a.DaysGoalMet != 1 {
		t.Errorf("a = %+v", a)
	}
	if _, ok := totals["b"]; ok {
		t.Error("b has no steps and must be missing, not invented")
	}
}

func TestErrorsAreReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	if _, err := NewActivity(srv.URL).Totals(context.Background(), TotalsRequest{}); err == nil {
		t.Error("Totals: a 500 must be an error")
	}
	if _, err := NewAuth(srv.URL).Names(context.Background(), []string{"a"}); err == nil {
		t.Error("Names: a 500 must be an error")
	}

	srv.Close() // now nothing is listening
	if _, err := NewActivity(srv.URL).Totals(context.Background(), TotalsRequest{}); err == nil {
		t.Error("Totals: a dead service must be an error")
	}
}

func TestNamesAsksInBatches(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var in struct {
			IDs []string `json:"ids"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if len(in.IDs) > nameBatch {
			t.Errorf("one request asked for %d names", len(in.IDs))
		}
		users := []map[string]string{}
		for _, id := range in.IDs {
			users = append(users, map[string]string{"id": id, "name": "name of " + id})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"users": users})
	}))
	defer srv.Close()

	ids := make([]string, nameBatch*2+1)
	for i := range ids {
		ids[i] = fmt.Sprintf("u%d", i)
	}
	names, err := NewAuth(srv.URL).Names(context.Background(), ids)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Errorf("made %d requests, want 3", calls)
	}
	if len(names) != len(ids) || names["u1000"] != "name of u1000" {
		t.Errorf("got %d names", len(names))
	}
}

func TestNamesWithNoIDsMakesNoRequest(t *testing.T) {
	names, err := NewAuth("http://127.0.0.1:1").Names(context.Background(), nil)
	if err != nil || len(names) != 0 {
		t.Fatalf("names=%v err=%v", names, err)
	}
}
