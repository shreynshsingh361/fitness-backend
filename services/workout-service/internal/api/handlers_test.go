package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"fitness/workout-service/internal/store"
)

func init() { gin.SetMode(gin.TestMode) }

const (
	someUser     = "11111111-1111-1111-1111-111111111111"
	someExercise = "22222222-2222-2222-2222-222222222222"
	someOpID     = "33333333-3333-3333-3333-333333333333"
)

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
	for _, path := range []string{"/api/v1/exercises", "/api/v1/workouts", "/api/v1/workouts/summary", "/api/v1/records"} {
		if got := do("GET", path, "", nil).Code; got != http.StatusUnauthorized {
			t.Errorf("GET %s without X-User-ID: status = %d, want 401", path, got)
		}
	}
}

func TestHealthIsPublic(t *testing.T) {
	if got := do("GET", "/health", "", nil).Code; got != http.StatusOK {
		t.Fatalf("status = %d, want 200", got)
	}
}

func TestCreateWorkoutRejectsBadInput(t *testing.T) {
	user := map[string]string{"X-User-ID": someUser}
	cases := []struct {
		name string
		body string
		want int
	}{
		{"not json", `{oops`, http.StatusBadRequest},
		{"no sets", `{"client_op_id":"` + someOpID + `","name":"Legs","sets":[]}`, http.StatusUnprocessableEntity},
		{"no name", `{"client_op_id":"` + someOpID + `","name":" ","sets":[{"exercise_id":"` + someExercise + `","reps":5,"weight_kg":50}]}`, http.StatusUnprocessableEntity},
		{"bad op id", `{"client_op_id":"abc","name":"Legs","sets":[{"exercise_id":"` + someExercise + `","reps":5,"weight_kg":50}]}`, http.StatusUnprocessableEntity},
		{"zero reps", `{"client_op_id":"` + someOpID + `","name":"Legs","sets":[{"exercise_id":"` + someExercise + `","reps":0,"weight_kg":50}]}`, http.StatusUnprocessableEntity},
		{"negative weight", `{"client_op_id":"` + someOpID + `","name":"Legs","sets":[{"exercise_id":"` + someExercise + `","reps":5,"weight_kg":-1}]}`, http.StatusUnprocessableEntity},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := do("POST", "/api/v1/workouts", tc.body, user).Code; got != tc.want {
				t.Fatalf("status = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestInvalidWorkoutIDIsNotFound(t *testing.T) {
	user := map[string]string{"X-User-ID": someUser}
	if got := do("GET", "/api/v1/workouts/not-a-uuid", "", user).Code; got != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", got)
	}
}

func TestInternalDeleteRejectsBadID(t *testing.T) {
	if got := do("DELETE", "/internal/users/not-a-uuid", "", nil).Code; got != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", got)
	}
}

func TestValidateWorkoutAcceptsGoodInput(t *testing.T) {
	in := store.NewWorkout{
		ClientOpID: someOpID,
		Name:       "Push day",
		Sets:       []store.NewSet{{ExerciseID: someExercise, Reps: 8, WeightKg: 60}},
	}
	if problems := validateWorkout(in); len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
}

func TestPageParams(t *testing.T) {
	cases := []struct {
		query               string
		page, limit, offset int
	}{
		{"", 1, 20, 0},
		{"?page=3&limit=10", 3, 10, 20},
		{"?page=0&limit=-5", 1, 20, 0},
		{"?limit=9999", 1, 100, 0},
		{"?page=abc", 1, 20, 0},
	}
	for _, tc := range cases {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("GET", "/x"+tc.query, nil)
		p, l, o := pageParams(c)
		if p != tc.page || l != tc.limit || o != tc.offset {
			t.Errorf("%q: got page=%d limit=%d offset=%d, want %d %d %d", tc.query, p, l, o, tc.page, tc.limit, tc.offset)
		}
	}
}
