package api

import (
	"net/http"
	"testing"
)

func TestForeignAmountAPI(t *testing.T) {
	h := newTestServer(t)
	wallet := seedBal(t, h, "Wallet", "payment_account")
	gold := seedBal(t, h, "Gold", "other_asset")
	body := func(extra string) string {
		return `{"type":"expense","amount":"850","currency":"THB","balance_uid":"` + wallet + `","spent_at":"2026-10-07T10:00:00+08:00"` + extra + `}`
	}

	rec, r := do(t, h, http.MethodPost, "/api/transactions", body(`,"foreign_amount":25.5,"foreign_currency":"usd"`))
	if rec.Code != http.StatusCreated || r["foreign_amount"] != "25.50" || r["foreign_amount_minor"] != 2550.0 || r["foreign_currency"] != "USD" ||
		r["amount"] != "850.00" || r["conversion_rates"] != nil {
		t.Fatalf("create: %d %v", rec.Code, r)
	}
	uid := r["uid"].(string)
	if _, b := do(t, h, http.MethodGet, "/api/balances/"+wallet, ""); b["balance"] != "-850.00" {
		t.Errorf("wallet = %v, want -850.00 (foreign amount must not count)", b["balance"])
	}
	if _, g := do(t, h, http.MethodGet, "/api/transactions/"+uid, ""); g["foreign_amount"] != "25.50" || g["foreign_currency"] != "USD" {
		t.Errorf("get: %v", g)
	}

	// PUT is a full replace: omitting the foreign fields clears them.
	rec, p := do(t, h, http.MethodPut, "/api/transactions/"+uid, body(`,"note":"no fx"`))
	if rec.Code != http.StatusOK || p["foreign_amount"] != nil || p["foreign_amount_minor"] != nil || p["foreign_currency"] != nil {
		t.Errorf("put without foreign: %d %v", rec.Code, p)
	}
	rec, p = do(t, h, http.MethodPut, "/api/transactions/"+uid, body(`,"foreign_amount":"3800","foreign_currency":"JPY"`))
	if rec.Code != http.StatusOK || p["foreign_amount"] != "3800" || p["foreign_currency"] != "JPY" {
		t.Errorf("put with foreign: %d %v", rec.Code, p)
	}

	cases := []struct{ name, body, field string }{
		{"same currency", body(`,"foreign_amount":"10","foreign_currency":"THB"`), "foreign_currency"},
		{"missing currency", body(`,"foreign_amount":"10"`), "foreign_currency"},
		{"missing amount", body(`,"foreign_currency":"USD"`), "foreign_amount"},
		{"transfer", `{"type":"transfer","amount":"10","currency":"THB","balance_uid":"` + wallet + `","to_balance_uid":"` + gold + `","spent_at":"2026-10-07T10:00:00+08:00","foreign_amount":"1","foreign_currency":"USD"}`, "foreign_amount"},
	}
	for _, c := range cases {
		rec, b := do(t, h, http.MethodPost, "/api/transactions", c.body)
		f, _ := b["fields"].(map[string]any)
		if rec.Code != http.StatusUnprocessableEntity || f[c.field] == nil {
			t.Errorf("%s: %d %v", c.name, rec.Code, b)
		}
	}

	// Bulk import carries the foreign amount too.
	rec, b := do(t, h, http.MethodPost, "/api/transactions/bulk", bulkBody("",
		`{"type":"income","amount":"3300","currency":"THB","balance_uid":"`+wallet+`","spent_at":"2026-10-08T10:00:00+08:00","foreign_amount":"100","foreign_currency":"USD"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("bulk: %d %s", rec.Code, rec.Body)
	}
	tx, _ := results(t, b)[0]["transaction"].(map[string]any)
	if tx["foreign_amount"] != "100.00" || tx["foreign_currency"] != "USD" {
		t.Errorf("bulk item: %v", tx)
	}
}
