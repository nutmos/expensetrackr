package api

import (
	"net/http"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gin-gonic/gin"
	"github.com/nutmos/expensetrackr/pkg/store/storetest"
)

// TestBalancesAndTransactionsArePerUser: user B cannot list, get, update or
// delete A's balances or transactions (404), including with a Bearer token.
// B cannot create a transaction using A's balance or category. Lists show
// only each user's own rows.
func TestBalancesAndTransactionsArePerUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	st := storetest.Open(t)
	router := (&Server{Store: st, Static: fstest.MapFS{"index.html": {Data: []byte("x")}}}).Router()
	alice := asUser(t, router, st, "alice")
	bob := asUser(t, router, st, "bob")

	cash := seedPayable(t, alice, "Cash", "THB")
	food := mkCat(t, alice, "Food", "expense")["uid"].(string)
	rec, body := do(t, alice, "POST", "/api/transactions",
		`{"amount":"10","currency":"THB","balance_uid":"`+cash+`","spent_at":"2026-10-01T10:00:00+07:00","category_uid":"`+food+`","note":"lunch"}`)
	if rec.Code != 201 {
		t.Fatalf("alice tx: %d %v", rec.Code, body)
	}
	txUID := body["uid"].(string)
	seedPayable(t, bob, "Cash", "THB") // same name OK for another user

	if _, l := do(t, bob, "GET", "/api/balances", ""); l["count"].(float64) != 1 {
		t.Errorf("bob balance list: %v", l)
	}
	if _, l := do(t, alice, "GET", "/api/balances", ""); l["count"].(float64) != 1 {
		t.Errorf("alice balance list: %v", l)
	}
	if _, l := do(t, bob, "GET", "/api/transactions", ""); l["count"].(float64) != 0 {
		t.Errorf("bob tx list: %v", l)
	}
	if _, l := do(t, alice, "GET", "/api/transactions", ""); l["count"].(float64) != 1 {
		t.Errorf("alice tx list: %v", l)
	}

	balPath := "/api/balances/" + cash
	txPath := "/api/transactions/" + txUID
	for _, m := range []struct{ method, path, body string }{
		{"GET", balPath, ""},
		{"PUT", balPath, `{"name":"Cash","type":"payment_account","currency":"THB","balance":"1"}`},
		{"DELETE", balPath, ""},
		{"GET", txPath, ""},
		{"PUT", txPath, `{"amount":"1","currency":"THB","balance_uid":"` + cash + `","spent_at":"2026-10-01T10:00:00+07:00"}`},
		{"DELETE", txPath, ""},
	} {
		if rec, _ := do(t, bob, m.method, m.path, m.body); rec.Code != http.StatusNotFound {
			t.Errorf("bob %s %s: %d", m.method, m.path, rec.Code)
		}
	}

	_, bobBalances := do(t, bob, "GET", "/api/balances", "")
	bobCash := bobBalances["balances"].([]any)[0].(map[string]any)["uid"].(string)
	cases := []struct {
		name  string
		body  string
		field string
	}{
		{"alice balance", `{"amount":"1","currency":"THB","balance_uid":"` + cash + `","spent_at":"2026-10-01T10:00:00+07:00"}`, "balance_uid"},
		{"alice category", `{"amount":"1","currency":"THB","balance_uid":"` + bobCash + `","spent_at":"2026-10-01T10:00:00+07:00","category_uid":"` + food + `"}`, "category_uid"},
	}
	for _, c := range cases {
		rec, b := do(t, bob, "POST", "/api/transactions", c.body)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status %d %v", c.name, rec.Code, b)
			continue
		}
		fields, _ := b["fields"].(map[string]any)
		if fields == nil || fields[c.field] == nil {
			t.Errorf("%s: fields %v", c.name, b)
		}
	}

	// Bearer token for bob: same 404s on Alice's resources.
	tok := createTok(t, &authClient{t: t, h: bob}, `{"name":"bob-agent","scopes":["read","write"]}`)
	anon := &authClient{t: t, h: router}
	for _, path := range []string{balPath, txPath} {
		if rec := anon.req("GET", path, "", bearer(tok.Token)); rec.Code != http.StatusNotFound {
			t.Errorf("bearer bob GET %s: %d", path, rec.Code)
		}
	}
	if rec := anon.req("GET", "/api/transactions", "", bearer(tok.Token)); !strings.Contains(rec.Body.String(), `"count":0`) {
		t.Errorf("bearer bob list: %s", rec.Body)
	}
}
