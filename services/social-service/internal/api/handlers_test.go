package api

import (
	"context"
	"encoding/csv"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"fitness/social-service/internal/clients"
)

func init() { gin.SetMode(gin.TestMode) }

const (
	someUser  = "11111111-1111-1111-1111-111111111111"
	otherUser = "22222222-2222-2222-2222-222222222222"
	someGroup = "33333333-3333-3333-3333-333333333333"
)

type noSteps struct{}

func (noSteps) Totals(context.Context, clients.TotalsRequest) (map[string]clients.UserTotal, error) {
	return nil, nil
}

type noNames struct{}

func (noNames) Names(context.Context, []string) (map[string]string, error) { return nil, nil }

// These tests never reach the database: requests are rejected before any SQL would
// run, so the store can be nil. The flows that need a database are covered by the
// smoke test in scripts/smoke-test.sh.
func do(method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	NewRouter(nil, noSteps{}, noNames{}).ServeHTTP(w, req)
	return w
}

var asUser = map[string]string{"X-User-ID": someUser}

func TestEverythingUnderV1NeedsAUser(t *testing.T) {
	paths := []string{
		"GET /api/v1/friends", "GET /api/v1/friends/invite", "GET /api/v1/friends/hidden",
		"POST /api/v1/friends/accept", "GET /api/v1/nudges", "POST /api/v1/nudges",
		"GET /api/v1/groups", "POST /api/v1/groups", "POST /api/v1/groups/join",
		"GET /api/v1/groups/" + someGroup, "PATCH /api/v1/groups/" + someGroup,
		"GET /api/v1/groups/" + someGroup + "/leaderboard",
		"GET /api/v1/groups/" + someGroup + "/report.csv",
		"GET /api/v1/groups/" + someGroup + "/messages",
	}
	for _, p := range paths {
		method, path, _ := strings.Cut(p, " ")
		if got := do(method, path, `{}`, nil).Code; got != http.StatusUnauthorized {
			t.Errorf("%s without X-User-ID: status = %d, want 401", p, got)
		}
	}
}

func TestBadIDsAreNotFound(t *testing.T) {
	for _, p := range []string{
		"DELETE /api/v1/friends/nope",
		"POST /api/v1/friends/nope/hide",
		"DELETE /api/v1/friends/nope/hide",
		"GET /api/v1/groups/nope",
		"GET /api/v1/groups/nope/leaderboard",
		"GET /api/v1/groups/nope/messages",
	} {
		method, path, _ := strings.Cut(p, " ")
		if got := do(method, path, `{}`, asUser).Code; got != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", p, got)
		}
	}
	if got := do("DELETE", "/internal/users/nope", "", nil).Code; got != http.StatusNotFound {
		t.Errorf("internal delete: status = %d, want 404", got)
	}
}

func TestCannotHideYourself(t *testing.T) {
	if got := do("POST", "/api/v1/friends/"+someUser+"/hide", "", asUser).Code; got != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", got)
	}
}

func TestBoardRejectsBadQueries(t *testing.T) {
	for _, path := range []string{
		"/api/v1/friends?period=year",
		"/api/v1/friends?period=challenge",
		"/api/v1/friends?date=nope",
	} {
		if got := do("GET", path, "", asUser).Code; got != http.StatusUnprocessableEntity {
			t.Errorf("GET %s: status = %d, want 422", path, got)
		}
	}
}

func TestAcceptAndJoinNeedACode(t *testing.T) {
	for _, path := range []string{"/api/v1/friends/accept", "/api/v1/groups/join"} {
		if got := do("POST", path, `{"code":"  "}`, asUser).Code; got != http.StatusUnprocessableEntity {
			t.Errorf("%s with a blank code: status = %d, want 422", path, got)
		}
		if got := do("POST", path, `{oops`, asUser).Code; got != http.StatusBadRequest {
			t.Errorf("%s with bad JSON: status = %d, want 400", path, got)
		}
	}
}

func TestCreateGroupValidation(t *testing.T) {
	long := strings.Repeat("x", 81)
	for name, body := range map[string]string{
		"no name":   `{"name":"   "}`,
		"long name": `{"name":"` + long + `"}`,
		"long desc": `{"name":"ok","description":"` + strings.Repeat("x", 501) + `"}`,
	} {
		if got := do("POST", "/api/v1/groups", body, asUser).Code; got != http.StatusUnprocessableEntity {
			t.Errorf("%s: status = %d, want 422", name, got)
		}
	}
}

func TestValidateGroupTextCountsCharactersNotBytes(t *testing.T) {
	name := strings.Repeat("क", 80) // 240 bytes, but 80 characters
	if problems := validateGroupText(&name, nil); len(problems) != 0 {
		t.Errorf("80 Hindi characters must fit: %v", problems)
	}
}

func TestValidateGroupTextIgnoresFieldsNotSent(t *testing.T) {
	if problems := validateGroupText(nil, nil); len(problems) != 0 {
		t.Errorf("a patch with no text fields must pass: %v", problems)
	}
}

func TestValidateNudge(t *testing.T) {
	group := someGroup
	badGroup := "nope"
	cases := []struct {
		name  string
		in    nudgeInput
		field string // the field expected to be reported; empty means valid
	}{
		{"valid", nudgeInput{ToUserID: otherUser, Kind: "cheer"}, ""},
		{"valid with group", nudgeInput{ToUserID: otherUser, Kind: "taunt", GroupID: &group}, ""},
		{"bad target", nudgeInput{ToUserID: "nope", Kind: "cheer"}, "to_user_id"},
		{"yourself", nudgeInput{ToUserID: someUser, Kind: "cheer"}, "to_user_id"},
		{"unknown kind", nudgeInput{ToUserID: otherUser, Kind: "slap"}, "kind"},
		{"bad group", nudgeInput{ToUserID: otherUser, Kind: "cheer", GroupID: &badGroup}, "group_id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			problems := validateNudge(tc.in, someUser)
			if tc.field == "" && len(problems) != 0 {
				t.Errorf("unexpected problems: %v", problems)
			}
			if tc.field != "" && problems[tc.field] == "" {
				t.Errorf("expected a problem with %s, got %v", tc.field, problems)
			}
		})
	}
}

func TestValidateChallenge(t *testing.T) {
	cases := []struct {
		name       string
		start, end string
		ok         bool
	}{
		{"one day is fine", "2026-11-07", "2026-11-07", true},
		{"a whole leap year is fine", "2028-01-01", "2028-12-31", true},
		{"more than a year", "2026-01-01", "2027-01-02", false},
		{"ends before it starts", "2026-11-07", "2026-11-06", false},
		{"bad start", "nope", "2026-11-06", false},
		{"bad end", "2026-11-06", "nope", false},
		{"missing", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, problems := validateChallenge(tc.start, tc.end)
			if (len(problems) == 0) != tc.ok {
				t.Errorf("problems = %v, want ok=%v", problems, tc.ok)
			}
		})
	}
}

func TestDaysBetweenCountsBothEnds(t *testing.T) {
	start, end, _ := validateChallenge("2026-11-02", "2026-11-08")
	if got := daysBetween(start, end); got != 7 {
		t.Errorf("Monday to Sunday = %d days, want 7", got)
	}
	if got := daysBetween(start, start); got != 1 {
		t.Errorf("a single day = %d, want 1", got)
	}
}

func TestCSVSafeNeutralizesFormulas(t *testing.T) {
	for in, want := range map[string]string{
		"Asha":                          "Asha",
		`=HYPERLINK("http://evil","x")`: `'=HYPERLINK("http://evil","x")`,
		"+91 Riya":                      "'+91 Riya",
		"-1+1":                          "'-1+1",
		"@SUM(A1)":                      "'@SUM(A1)",
		"":                              "",
		"Riya = best":                   "Riya = best", // only the start of a cell is dangerous
	} {
		if got := csvSafe(in); got != want {
			t.Errorf("csvSafe(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWriteReport(t *testing.T) {
	var out strings.Builder
	err := writeReport(&out, []reportRow{
		{Rank: 1, Name: "Asha, the \"fast\" one", Steps: 7000, Average: 1000, DaysActive: 5, BestDay: 3000, DaysGoalMet: 2},
		{Rank: 2, Name: "=EVIL()", Steps: 100, Average: 14, DaysActive: 1, BestDay: 100},
	})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(strings.NewReader(out.String())).ReadAll()
	if err != nil {
		t.Fatalf("the output is not valid CSV: %v\n%s", err, out.String())
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want a header and 2 people", len(rows))
	}
	if rows[0][0] != "rank" || rows[0][1] != "name" || rows[0][2] != "total_steps" {
		t.Errorf("header = %v", rows[0])
	}
	if rows[1][1] != `Asha, the "fast" one` || rows[1][2] != "7000" || rows[1][6] != "2" {
		t.Errorf("row 1 = %v (commas and quotes must survive)", rows[1])
	}
	if rows[2][1] != "'=EVIL()" {
		t.Errorf("row 2 name = %q, want the formula neutralized", rows[2][1])
	}
}

func TestPageParams(t *testing.T) {
	cases := []struct {
		query               string
		page, limit, offset int
	}{
		{"", 1, 50, 0},
		{"?page=3&limit=10", 3, 10, 20},
		{"?page=0&limit=-5", 1, 50, 0},
		{"?limit=9999", 1, 100, 0},
	}
	for _, tc := range cases {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("GET", "/x"+tc.query, nil)
		p, l, o := pageParams(c)
		if p != tc.page || l != tc.limit || o != tc.offset {
			t.Errorf("%q: got page=%d limit=%d offset=%d", tc.query, p, l, o)
		}
	}
}
