package api

import (
	"net/http"
	"strings"
	"testing"
)

const goodRecipe = `{"name":"Mom's dal","portions":4,"cooked_weight_g":1200,
	"ingredients":[{"food_id":"` + someFood + `","servings":2.5}]}`

func TestRecipesNeedAUser(t *testing.T) {
	for _, req := range []struct{ method, path string }{
		{"GET", "/api/v1/recipes"},
		{"POST", "/api/v1/recipes"},
		{"GET", "/api/v1/recipes/" + someFood},
		{"PUT", "/api/v1/recipes/" + someFood},
		{"DELETE", "/api/v1/recipes/" + someFood},
	} {
		if got := do(req.method, req.path, goodRecipe, nil).Code; got != http.StatusUnauthorized {
			t.Errorf("%s %s without X-User-ID: status = %d, want 401", req.method, req.path, got)
		}
	}
}

func TestRecipeWithBadIDIsNotFound(t *testing.T) {
	user := map[string]string{"X-User-ID": someUser}
	for _, method := range []string{"GET", "PUT", "DELETE"} {
		if got := do(method, "/api/v1/recipes/nope", goodRecipe, user).Code; got != http.StatusNotFound {
			t.Errorf("%s /recipes/nope: status = %d, want 404", method, got)
		}
	}
}

func TestSaveRecipeRejectsBadInput(t *testing.T) {
	user := map[string]string{"X-User-ID": someUser}
	ingredient := `{"food_id":"` + someFood + `","servings":1}`
	many := strings.TrimSuffix(strings.Repeat(ingredient+",", 51), ",")

	cases := []struct {
		name, body string
		want       int
		field      string // the field named in the error details
	}{
		{"not json", `{oops`, http.StatusBadRequest, ""},
		{"no name", `{"name":"  ","portions":1,"ingredients":[` + ingredient + `]}`, 422, "name"},
		{"long name", `{"name":"` + strings.Repeat("x", 101) + `","portions":1,"ingredients":[` + ingredient + `]}`, 422, "name"},
		{"zero portions", `{"name":"Dal","portions":0,"ingredients":[` + ingredient + `]}`, 422, "portions"},
		{"too many portions", `{"name":"Dal","portions":101,"ingredients":[` + ingredient + `]}`, 422, "portions"},
		{"weightless dish", `{"name":"Dal","portions":1,"cooked_weight_g":0,"ingredients":[` + ingredient + `]}`, 422, "cooked_weight_g"},
		{"no ingredients", `{"name":"Dal","portions":1,"ingredients":[]}`, 422, "ingredients"},
		{"51 ingredients", `{"name":"Dal","portions":1,"ingredients":[` + many + `]}`, 422, "ingredients"},
		{"bad food id", `{"name":"Dal","portions":1,"ingredients":[{"food_id":"nope","servings":1}]}`, 422, "ingredients[0].food_id"},
		{"too little", `{"name":"Dal","portions":1,"ingredients":[{"food_id":"` + someFood + `","servings":0.001}]}`, 422, "ingredients[0].servings"},
		{"too much", `{"name":"Dal","portions":1,"ingredients":[{"food_id":"` + someFood + `","servings":1001}]}`, 422, "ingredients[0].servings"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, req := range []struct{ method, path string }{
				{"POST", "/api/v1/recipes"},
				{"PUT", "/api/v1/recipes/" + someFood},
			} {
				w := do(req.method, req.path, tc.body, user)
				if w.Code != tc.want {
					t.Fatalf("%s: status = %d, want %d", req.method, w.Code, tc.want)
				}
				if tc.field != "" && !strings.Contains(w.Body.String(), `"`+tc.field+`"`) {
					t.Errorf("%s: details do not name %s: %s", req.method, tc.field, w.Body.String())
				}
			}
		})
	}
}

func TestValidRecipeIsCleanedUp(t *testing.T) {
	var in recipeInput
	in.Name = "  Mom's dal "
	in.Portions = 4
	in.Ingredients = append(in.Ingredients, struct {
		FoodID   string  `json:"food_id"`
		Servings float64 `json:"servings"`
	}{FoodID: strings.ToUpper(someFood), Servings: 1000})

	out, problems := validateRecipe(in)
	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
	if out.Name != "Mom's dal" {
		t.Errorf("name = %q, want it trimmed", out.Name)
	}
	if out.Ingredients[0].FoodID != someFood {
		t.Errorf("food id = %q, want the lower-case form %q", out.Ingredients[0].FoodID, someFood)
	}
	if out.CookedWeightG != nil {
		t.Errorf("cooked weight = %v, want none", *out.CookedWeightG)
	}
}
