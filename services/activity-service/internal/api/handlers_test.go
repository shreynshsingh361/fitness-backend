package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"fitness/activity-service/internal/stats"
)

func init() { gin.SetMode(gin.TestMode) }

const someUser = "11111111-1111-1111-1111-111111111111"

func day(s string) time.Time {
	d, err := time.Parse(dateLayout, s)
	if err != nil {
		panic(err)
	}
	return d
}

// These tests never reach the database: requests are rejected before any SQL
// would run, so the store can be nil.
func do(method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	NewRouter(nil).ServeHTTP(w, req)
	return w
}

func TestEverythingUnderV1NeedsAUser(t *testing.T) {
	for _, path := range []string{"/api/v1/steps/today", "/api/v1/steps/history", "/api/v1/steps/stats", "/api/v1/steps/settings"} {
		if got := do("GET", path, "", nil).Code; got != http.StatusUnauthorized {
			t.Errorf("GET %s without X-User-ID: status = %d, want 401", path, got)
		}
	}
}

func TestSyncRejectsBadInput(t *testing.T) {
	user := map[string]string{"X-User-ID": someUser}
	today := time.Now().UTC().Format(dateLayout)
	cases := []struct {
		name string
		body string
		want int
	}{
		{"not json", `{oops`, http.StatusBadRequest},
		{"no days", `{"source":"fitbit","days":[]}`, http.StatusUnprocessableEntity},
		{"unknown source", `{"source":"my_watch","days":[{"date":"` + today + `","steps":100}]}`, http.StatusUnprocessableEntity},
		{"manual is not a device", `{"source":"manual","days":[{"date":"` + today + `","steps":100}]}`, http.StatusUnprocessableEntity},
		{"negative steps", `{"source":"fitbit","days":[{"date":"` + today + `","steps":-1}]}`, http.StatusUnprocessableEntity},
		{"absurd steps", `{"source":"fitbit","days":[{"date":"` + today + `","steps":999999}]}`, http.StatusUnprocessableEntity},
		{"bad date", `{"source":"fitbit","days":[{"date":"yesterday","steps":100}]}`, http.StatusUnprocessableEntity},
		{"far future", `{"source":"fitbit","days":[{"date":"2099-01-01","steps":100}]}`, http.StatusUnprocessableEntity},
		{"too old", `{"source":"fitbit","days":[{"date":"2010-01-01","steps":100}]}`, http.StatusUnprocessableEntity},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := do("POST", "/api/v1/steps/sync", tc.body, user).Code; got != tc.want {
				t.Fatalf("status = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestSyncAcceptsAYearOfHistory(t *testing.T) {
	back := time.Now().UTC().AddDate(0, 0, -400).Format(dateLayout)
	body := `{"source":"health_connect","days":[{"date":"` + back + `","steps":4321}]}`
	_, problems := validateSync(syncInput{Source: "health_connect", Days: []struct {
		Date  string `json:"date"`
		Steps int    `json:"steps"`
	}{{Date: back, Steps: 4321}}}, time.Now())
	if len(problems) > 0 {
		t.Fatalf("a day 400 days back must be accepted (history backfill), got %v for %s", problems, body)
	}
}

func TestSyncCarriesAtMostSixtyDays(t *testing.T) {
	days := ""
	for i := 0; i < 61; i++ {
		if i > 0 {
			days += ","
		}
		days += `{"date":"` + time.Now().UTC().AddDate(0, 0, -i).Format(dateLayout) + `","steps":100}`
	}
	body := `{"source":"health_connect","days":[` + days + `]}`
	if got := do("POST", "/api/v1/steps/sync", body, map[string]string{"X-User-ID": someUser}).Code; got != http.StatusUnprocessableEntity {
		t.Fatalf("61 days in one sync: status = %d, want 422", got)
	}
}

func TestTypedInStepsAreGone(t *testing.T) {
	user := map[string]string{"X-User-ID": someUser}
	today := time.Now().UTC().Format(dateLayout)
	if got := do("PUT", "/api/v1/steps/manual", `{"date":"`+today+`","steps":5000}`, user).Code; got != http.StatusNotFound {
		t.Fatalf("PUT /steps/manual: status = %d, want 404", got)
	}
}

func TestCheckDayAllowsEveryTimeZoneButNotTheFuture(t *testing.T) {
	// 20:00 UTC on Nov 7 is already Nov 8 in Auckland (UTC+13) and on Kiribati (UTC+14).
	now := time.Date(2026, 11, 7, 20, 0, 0, 0, time.UTC)

	if _, err := checkDay("2026-11-08", 30, now); err != nil {
		t.Errorf("Nov 8 is today in Kiribati, must be allowed: %v", err)
	}
	if _, err := checkDay("2026-11-09", 30, now); err == nil {
		t.Error("Nov 9 is the future everywhere, must be rejected")
	}
	if _, err := checkDay("2026-10-08", 30, now); err != nil {
		t.Errorf("30 days back must be allowed: %v", err)
	}
	if _, err := checkDay("2026-10-01", 30, now); err == nil {
		t.Error("more than 30 days back must be rejected")
	}
}

func TestBadQueryDates(t *testing.T) {
	user := map[string]string{"X-User-ID": someUser}
	for _, path := range []string{
		"/api/v1/steps/today?date=nope",
		"/api/v1/steps/stats?date=2026-13-45",
		"/api/v1/steps/history?to=nope",
		"/api/v1/steps/history?from=nope&to=2026-11-07",
		"/api/v1/steps/history?from=2026-11-08&to=2026-11-07",
		"/api/v1/steps/history?from=2020-01-01&to=2026-11-07",
	} {
		if got := do("GET", path, "", user).Code; got != http.StatusUnprocessableEntity {
			t.Errorf("GET %s: status = %d, want 422", path, got)
		}
	}
}

func TestValidateSettings(t *testing.T) {
	text := func(s string) *string { return &s }

	if _, problems := validateSettings(settingsInput{DailyGoal: 500}); problems["daily_goal"] == "" {
		t.Error("a goal below 1000 must be rejected")
	}
	if _, problems := validateSettings(settingsInput{DailyGoal: 200000}); problems["daily_goal"] == "" {
		t.Error("a goal above 100000 must be rejected")
	}
	if _, problems := validateSettings(settingsInput{DailyGoal: 8000, Country: text("India")}); problems["country"] == "" {
		t.Error("a country name instead of a code must be rejected")
	}

	country, problems := validateSettings(settingsInput{DailyGoal: 8000, Country: text(" in ")})
	if len(problems) != 0 || country == nil || *country != "IN" {
		t.Errorf("got country=%v problems=%v, want IN", country, problems)
	}
	country, problems = validateSettings(settingsInput{DailyGoal: 8000, Country: text("")})
	if len(problems) != 0 || country != nil {
		t.Errorf("empty country must clear it: got %v %v", country, problems)
	}
}

func TestBuildHistoryFillsGapsAndAverages(t *testing.T) {
	totals := []stats.Day{
		{Date: day("2026-10-01"), Steps: 99999}, // outside the range
		{Date: day("2026-11-02"), Steps: 1000},
		{Date: day("2026-11-04"), Steps: 2000},
	}
	got := buildHistory(totals, 1500, day("2026-11-02"), day("2026-11-05"))

	if len(got.Days) != 4 {
		t.Fatalf("got %d days, want 4", len(got.Days))
	}
	if got.Days[1].Date != "2026-11-03" || got.Days[1].Steps != 0 {
		t.Errorf("the gap must be filled with zero: %+v", got.Days[1])
	}
	if got.Days[0].GoalMet || !got.Days[2].GoalMet {
		t.Errorf("goal_met wrong: %+v", got.Days)
	}
	if got.TotalSteps != 3000 || got.AverageSteps != 750 {
		t.Errorf("total=%d average=%d, want 3000 and 750", got.TotalSteps, got.AverageSteps)
	}
}

func TestInternalTotalsValidation(t *testing.T) {
	cases := map[string]string{
		"bad id":   `{"user_ids":["nope"],"from":"2026-11-01","to":"2026-11-07"}`,
		"bad from": `{"user_ids":[],"from":"nope","to":"2026-11-07"}`,
		"bad to":   `{"user_ids":[],"from":"2026-11-01","to":"nope"}`,
		"bad cut":  `{"user_ids":[],"from":"2026-11-01","to":"2026-11-07","reveal_before":"nope"}`,
	}
	for name, body := range cases {
		if got := do("POST", "/internal/steps/totals", body, nil).Code; got != http.StatusUnprocessableEntity {
			t.Errorf("%s: status = %d, want 422", name, got)
		}
	}
}

func TestInternalDeleteRejectsBadID(t *testing.T) {
	if got := do("DELETE", "/internal/users/not-a-uuid", "", nil).Code; got != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", got)
	}
}
