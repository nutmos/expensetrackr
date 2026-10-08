package api

import (
	"net/http"
	"strings"
	"testing"
)

func TestPageRoutes(t *testing.T) {
	h := newTestServer(t)
	const uid = "0b6c2f7e-3c1a-4d5e-9f00-1234567890ab"
	ok := []string{"/"}
	for _, res := range []string{"transactions", "balances", "categories"} {
		ok = append(ok,
			"/"+res,
			"/"+res+"?from=2026-10-01&to=2026-10-31",
			"/"+res+"/new",
			"/"+res+"/"+uid+"/edit",
			"/"+res+"/"+strings.ToUpper(uid)+"/edit",
		)
	}
	for _, p := range ok {
		rec, _ := do(t, h, "GET", p, "")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Expense Log") ||
			!strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
			t.Errorf("GET %s: %d %q %q", p, rec.Code, rec.Header().Get("Content-Type"), rec.Body)
		}
	}
	for _, p := range []string{
		"/transactions/abc/edit",         // malformed uid
		"/balances/" + uid,               // no bare detail page
		"/categories/" + uid + "/delete", // unknown action
		"/transactions/new/edit",         // "new" is not a uid
		"/budgets",                       // unknown resource
		"/budgets/new",
	} {
		if rec, _ := do(t, h, "GET", p, ""); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: %d, want 404", p, rec.Code)
		}
	}
	// Page paths only answer GET; the JSON API keeps its own routes.
	if rec, _ := do(t, h, "POST", "/transactions/new", "{}"); rec.Code != http.StatusNotFound {
		t.Errorf("POST page: %d, want 404", rec.Code)
	}
	if rec, body := do(t, h, "GET", "/api/transactions", ""); rec.Code != http.StatusOK || body["count"] == nil {
		t.Errorf("API still JSON: %d %v", rec.Code, body)
	}
}
