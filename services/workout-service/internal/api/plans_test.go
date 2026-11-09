package api

import (
	"net/http"
	"strings"
	"testing"

	"fitness/workout-service/internal/store"
)

const somePlanExercise = "44444444-4444-4444-4444-444444444444"

func TestPlansNeedAUser(t *testing.T) {
	for _, req := range []struct{ method, path string }{
		{"GET", "/api/v1/plans"},
		{"GET", "/api/v1/plans/back"},
		{"POST", "/api/v1/plans/back/exercises"},
		{"PUT", "/api/v1/plans/back/exercises/" + somePlanExercise},
		{"DELETE", "/api/v1/plans/back/exercises/" + somePlanExercise},
	} {
		if got := do(req.method, req.path, `{}`, nil).Code; got != http.StatusUnauthorized {
			t.Errorf("%s %s without X-User-ID: status = %d, want 401", req.method, req.path, got)
		}
	}
}

func TestUnknownBodyPartIsNotFound(t *testing.T) {
	user := map[string]string{"X-User-ID": someUser}
	for _, req := range []struct{ method, path string }{
		{"GET", "/api/v1/plans/neck"},
		{"POST", "/api/v1/plans/neck/exercises"},
		{"PUT", "/api/v1/plans/neck/exercises/" + somePlanExercise},
		{"DELETE", "/api/v1/plans/back/exercises/nope"},
	} {
		if got := do(req.method, req.path, `{}`, user).Code; got != http.StatusNotFound {
			t.Errorf("%s %s: status = %d, want 404", req.method, req.path, got)
		}
	}
}

func TestPlanExerciseRejectsBadInput(t *testing.T) {
	user := map[string]string{"X-User-ID": someUser}
	path := "/api/v1/plans/back/exercises/" + somePlanExercise
	set := `{"weight":20,"reps":10}`
	eleven := strings.TrimSuffix(strings.Repeat(set+",", 11), ",")

	cases := []struct{ name, body, field string }{
		{"bad unit", `{"unit":"stone","sets":[` + set + `]}`, "unit"},
		{"no sets", `{"unit":"kg","sets":[]}`, "sets"},
		{"eleven sets", `{"unit":"kg","sets":[` + eleven + `]}`, "sets"},
		{"negative weight", `{"unit":"kg","sets":[{"weight":-1,"reps":10}]}`, "sets[0].weight"},
		{"over 250 kg", `{"unit":"kg","sets":[` + set + `,{"weight":250.5,"reps":10}]}`, "sets[1].weight"},
		{"over 551 lb", `{"unit":"lb","sets":[{"weight":552,"reps":10}]}`, "sets[0].weight"},
		{"no reps", `{"unit":"lb","sets":[` + set + `,{"weight":20,"reps":0}]}`, "sets[1].reps"},
	}
	for _, tc := range cases {
		w := do("PUT", path, tc.body, user)
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `"`+tc.field+`"`) {
			t.Errorf("%s: status %d, body %s; want 422 naming %s", tc.name, w.Code, w.Body.String(), tc.field)
		}
	}
	if got := do("POST", "/api/v1/plans/back/exercises", `{"exercise_id":"nope"}`, user).Code; got != 422 {
		t.Errorf("adding a bad exercise id: status = %d, want 422", got)
	}
}

func TestTenSetsUpToTheHeaviestAreFine(t *testing.T) {
	for unit, heaviest := range map[string]float64{"kg": 250, "lb": 551} {
		var in planExerciseInput
		in.Unit = unit
		for range 10 {
			in.Sets = append(in.Sets, store.PlanSet{Weight: heaviest, Reps: 1000})
		}
		if problems := validatePlanExercise(in); len(problems) != 0 {
			t.Errorf("%s: unexpected problems %v", unit, problems)
		}
	}
}
