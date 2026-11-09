package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func init() { gin.SetMode(gin.TestMode) }

const (
	someUser = "11111111-1111-1111-1111-111111111111"
	someFood = "22222222-2222-2222-2222-222222222222"
	someOpID = "33333333-3333-3333-3333-333333333333"
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
	for _, path := range []string{"/api/v1/foods", "/api/v1/diary", "/api/v1/goals", "/api/v1/water", "/api/v1/nutrition/summary"} {
		if got := do("GET", path, "", nil).Code; got != http.StatusUnauthorized {
			t.Errorf("GET %s without X-User-ID: status = %d, want 401", path, got)
		}
	}
}

func TestCreateEntryRejectsBadInput(t *testing.T) {
	user := map[string]string{"X-User-ID": someUser}
	good := func(override string) string {
		// A valid body, with one field replaced by `override` (a JSON fragment).
		base := map[string]string{
			"client_op_id": `"` + someOpID + `"`,
			"food_id":      `"` + someFood + `"`,
			"meal":         `"lunch"`,
			"servings":     `1.5`,
			"eaten_on":     `"2026-11-07"`,
		}
		if override != "" {
			k, v, _ := strings.Cut(override, "=")
			base[k] = v
		}
		parts := []string{}
		for k, v := range base {
			parts = append(parts, `"`+k+`":`+v)
		}
		return "{" + strings.Join(parts, ",") + "}"
	}

	cases := []struct {
		name string
		body string
		want int
	}{
		{"not json", `{oops`, http.StatusBadRequest},
		{"bad meal", good(`meal="brunch"`), http.StatusUnprocessableEntity},
		{"zero servings", good(`servings=0`), http.StatusUnprocessableEntity},
		{"too many servings", good(`servings=500`), http.StatusUnprocessableEntity},
		{"too small", good(`servings=0.005`), http.StatusUnprocessableEntity},
		{"bad date", good(`eaten_on="07/11/2026"`), http.StatusUnprocessableEntity},
		{"bad food id", good(`food_id="nope"`), http.StatusUnprocessableEntity},
		{"bad op id", good(`client_op_id="nope"`), http.StatusUnprocessableEntity},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := do("POST", "/api/v1/diary", tc.body, user).Code; got != tc.want {
				t.Fatalf("status = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestValidEntryPassesValidation(t *testing.T) {
	day, problems := validateEntry(createEntryInput{
		ClientOpID: someOpID, FoodID: someFood, Meal: "snack", Servings: 0.5, EatenOn: "2026-11-07",
	})
	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
	if got := day.Format(dateLayout); got != "2026-11-07" {
		t.Fatalf("day = %s", got)
	}
}

// Grams typed in the app become servings, so they can be tiny or large:
// 5 g of a "100 g" serving is 0.05, and 100 g of a "2.5 g" serving is 40.
func TestGramSizedServingsPassValidation(t *testing.T) {
	for _, servings := range []float64{0.01, 0.05, 40, 100} {
		_, problems := validateEntry(createEntryInput{
			ClientOpID: someOpID, FoodID: someFood, Meal: "lunch", Servings: servings, EatenOn: "2026-11-07",
		})
		if len(problems) != 0 {
			t.Errorf("servings %v: unexpected problems %v", servings, problems)
		}
	}
}

func TestGoalValidation(t *testing.T) {
	user := map[string]string{"X-User-ID": someUser}
	for _, body := range []string{`{"calories":100}`, `{"calories":99999}`, `{}`} {
		if got := do("PUT", "/api/v1/goals", body, user).Code; got != http.StatusUnprocessableEntity {
			t.Errorf("body %s: status = %d, want 422", body, got)
		}
	}
}

func TestWaterValidation(t *testing.T) {
	user := map[string]string{"X-User-ID": someUser}
	for _, body := range []string{`{"amount_ml":0}`, `{"amount_ml":6000}`, `{"amount_ml":250,"date":"tomorrow"}`, `{"amount_ml":250,"client_op_id":"not-a-uuid"}`} {
		if got := do("POST", "/api/v1/water", body, user).Code; got != http.StatusUnprocessableEntity {
			t.Errorf("body %s: status = %d, want 422", body, got)
		}
	}
}

// A bad amount is refused before any SQL runs; above all it must never turn into a full reset.
func TestWaterRemovalValidation(t *testing.T) {
	user := map[string]string{"X-User-ID": someUser}
	for _, query := range []string{"amount_ml=0", "amount_ml=6000", "amount_ml=abc", "amount_ml=", "amount_ml=250&date=x", "amount_ml=250&client_op_id=nope"} {
		if got := do("DELETE", "/api/v1/water?"+query, "", user).Code; got != http.StatusUnprocessableEntity {
			t.Errorf("DELETE /water?%s: status = %d, want 422", query, got)
		}
	}
}

func TestInternalDeleteRejectsBadID(t *testing.T) {
	if got := do("DELETE", "/internal/users/not-a-uuid", "", nil).Code; got != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", got)
	}
}

func TestMonthNeedsAValidMonth(t *testing.T) {
	user := map[string]string{"X-User-ID": someUser}
	for _, query := range []string{"", "?month=", "?month=2026", "?month=2026-13", "?month=2026-11-07", "?month=nov"} {
		if got := do("GET", "/api/v1/diary/month"+query, "", user).Code; got != http.StatusUnprocessableEntity {
			t.Errorf("GET /diary/month%s: status = %d, want 422", query, got)
		}
	}
	if got := do("GET", "/api/v1/diary/month?month=2026-11", "", nil).Code; got != http.StatusUnauthorized {
		t.Errorf("without a user: status = %d, want 401", got)
	}
}

func TestBadDateQuery(t *testing.T) {
	user := map[string]string{"X-User-ID": someUser}
	for _, path := range []string{"/api/v1/diary?date=yesterday", "/api/v1/nutrition/summary?date=2026-13-45", "/api/v1/water?date=x"} {
		if got := do("GET", path, "", user).Code; got != http.StatusUnprocessableEntity {
			t.Errorf("GET %s: status = %d, want 422", path, got)
		}
	}
}
