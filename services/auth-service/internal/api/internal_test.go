package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const someUser = "11111111-1111-1111-1111-111111111111"

func TestLookupValidation(t *testing.T) {
	r := newTestRouter()

	if got := do(r, "POST", "/internal/users/lookup", `{oops`, nil).Code; got != http.StatusBadRequest {
		t.Errorf("bad JSON: status = %d, want 400", got)
	}
	if got := do(r, "POST", "/internal/users/lookup", `{"ids":["nope"]}`, nil).Code; got != http.StatusUnprocessableEntity {
		t.Errorf("bad id: status = %d, want 422", got)
	}

	tooMany := `{"ids":["` + strings.Repeat(someUser+`","`, maxLookupIDs) + someUser + `"]}`
	if got := do(r, "POST", "/internal/users/lookup", tooMany, nil).Code; got != http.StatusUnprocessableEntity {
		t.Errorf("too many ids: status = %d, want 422", got)
	}
}

func TestLookupWithNoIDsAnswersWithoutTheDatabase(t *testing.T) {
	w := do(newTestRouter(), "POST", "/internal/users/lookup", `{"ids":[]}`, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var out struct {
		Users []any `json:"users"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.Users == nil || len(out.Users) != 0 {
		t.Fatalf("users must be an empty list, not null: %s", w.Body.String())
	}
}

func TestInternalRoutesRejectBadIDs(t *testing.T) {
	r := newTestRouter()
	if got := do(r, "POST", "/internal/users/nope/verify-password", `{"password":"x"}`, nil).Code; got != http.StatusNotFound {
		t.Errorf("verify-password: status = %d, want 404", got)
	}
	if got := do(r, "DELETE", "/internal/users/nope", "", nil).Code; got != http.StatusNotFound {
		t.Errorf("delete: status = %d, want 404", got)
	}
}

func TestVerifyPasswordNeedsJSON(t *testing.T) {
	if got := do(newTestRouter(), "POST", "/internal/users/"+someUser+"/verify-password", `{oops`, nil).Code; got != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", got)
	}
}
