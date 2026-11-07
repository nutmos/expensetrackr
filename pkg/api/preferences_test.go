package api

import (
	"net/http"
	"testing"

	"github.com/nutmos/expensetrackr/pkg/user"
)

func TestPreferencesAPI(t *testing.T) {
	h, st := newTestServerWithStore(t)
	_, me := do(t, h, "GET", "/api/auth/me", "")
	path := "/api/users/" + me["uid"].(string) + "/preferences"

	// Default before anything is set; also part of the user response.
	if rec, p := do(t, h, "GET", path, ""); rec.Code != http.StatusOK || len(p) != 1 || p["base_currency"] != user.DefaultBaseCurrency {
		t.Fatalf("default: %d %v", rec.Code, p)
	}
	if _, u := do(t, h, "GET", "/api/auth/me", ""); u["preferences"].(map[string]any)["base_currency"] != user.DefaultBaseCurrency {
		t.Errorf("me preferences: %v", u["preferences"])
	}

	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantField  string // field error expected on a 400
		wantBase   string // GET afterwards
	}{
		{"set", `{"base_currency":"THB"}`, 200, "", "THB"},
		{"lowercase normalized", `{"base_currency":"sgd"}`, 200, "", "SGD"},
		{"unknown currency", `{"base_currency":"XYZ"}`, 400, "base_currency", "SGD"},
		{"wrong type", `{"base_currency":7}`, 400, "base_currency", "SGD"},
		{"unknown key", `{"theme":"dark"}`, 400, "preferences", "SGD"},
		{"not an object", `["THB"]`, 400, "preferences", "SGD"},
		{"malformed", `{"base_currency":`, 400, "", "SGD"},
		{"empty object resets to default", `{}`, 200, "", user.DefaultBaseCurrency},
		{"round-trip", `{"base_currency":"JPY"}`, 200, "", "JPY"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec, body := do(t, h, "PUT", path, tc.body)
			if rec.Code != tc.wantStatus {
				t.Fatalf("PUT %s: %d %v, want %d", tc.body, rec.Code, body, tc.wantStatus)
			}
			if tc.wantField != "" && fieldsOf(body)[tc.wantField] == nil {
				t.Errorf("PUT %s: missing field error %q: %v", tc.body, tc.wantField, body)
			}
			if _, p := do(t, h, "GET", path, ""); p["base_currency"] != tc.wantBase {
				t.Errorf("after %s: base_currency %v, want %s", tc.body, p["base_currency"], tc.wantBase)
			}
		})
	}

	// Scoped to the logged-in user: another user's preferences are a 404,
	// like an unknown user (issue #76).
	other := otherUser(t, st)
	otherPath := "/api/users/" + other.UID + "/preferences"
	for _, m := range []string{"GET", "PUT"} {
		if rec, _ := do(t, h, m, otherPath, `{"base_currency":"EUR"}`); rec.Code != http.StatusNotFound {
			t.Errorf("%s other user's preferences: %d, want 404", m, rec.Code)
		}
	}
	if rec, _ := do(t, h, "GET", "/api/users/00000000-0000-4000-8000-000000000000/preferences", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown user: %d, want 404", rec.Code)
	}
}
