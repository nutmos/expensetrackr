package api

import (
	"net/http"
	"testing"
)

// TestBeginningBalanceAPI (issue #172): POST /api/balances records the
// beginning balance; it is listed and filterable, editable with PUT (like a
// balance adjustment, but it stays on its balance and may be 0), and cannot
// be created through the transactions API or deleted.
func TestBeginningBalanceAPI(t *testing.T) {
	h := newTestServer(t)
	cash := seedPayable(t, h, "Cash", "THB") // balance 10000
	bank := seedPayable(t, h, "Bank", "THB")

	rec, b := do(t, h, "GET", "/api/transactions?type=beginning_balance&balance_uid="+cash, "")
	list, _ := b["transactions"].([]any)
	if rec.Code != 200 || len(list) != 1 {
		t.Fatalf("list: %d %v", rec.Code, b)
	}
	bb := list[0].(map[string]any)
	if bb["type"] != "beginning_balance" || bb["amount"] != "10000.00" || bb["adjustment_direction"] != "increase" ||
		bb["account"] != "Cash" || bb["currency"] != "THB" || bb["category_uid"] != nil || bb["version"] != float64(1) {
		t.Errorf("beginning balance: %v", bb)
	}
	path := "/api/transactions/" + bb["uid"].(string)
	balanceIs := func(want string) {
		t.Helper()
		if _, b := do(t, h, "GET", "/api/balances/"+cash, ""); b["balance"] != want {
			t.Errorf("cash = %v, want %s", b["balance"], want)
		}
	}

	// Not creatable, directly or in bulk.
	body := `{"type":"beginning_balance","amount":"5","currency":"THB","balance_uid":"` + cash + `","spent_at":"2026-10-08T12:00:00+08:00","adjustment_direction":"increase"}`
	if rec, b := do(t, h, "POST", "/api/transactions", body); rec.Code != 422 || fieldsOf(b)["type"] == nil {
		t.Errorf("POST: %d %v", rec.Code, b)
	}
	if rec, b := do(t, h, "POST", "/api/transactions/bulk", `{"transactions":[`+body+`]}`); rec.Code == http.StatusCreated {
		t.Errorf("bulk: %d %v", rec.Code, b)
	}

	// An earlier expense moves it back.
	mustTx(t, h, `{"amount":"100","currency":"THB","balance_uid":"`+cash+`","spent_at":"2026-10-01T09:00:00+07:00"}`)
	_, bb = do(t, h, "GET", path, "")
	if bb["spent_at"] != "2026-10-01T09:00:00+07:00" || bb["version"] != float64(2) {
		t.Errorf("after earlier expense: %v", bb)
	}
	balanceIs("9900.00")

	// Editable: amount, direction, date (not after the expense), note.
	edit := func(changes string, hdr map[string]string) (int, map[string]any) {
		t.Helper()
		rec, b := putMergedH(t, h, path, changes, hdr)
		return rec.Code, b
	}
	for _, c := range []struct {
		name, changes, field string
		code                 int
	}{
		{"type", `{"type":"expense"}`, "type", 422},
		{"other balance", `{"balance_uid":"` + bank + `"}`, "balance_uid", 422},
		{"after the expense", `{"spent_at":"2026-10-02T00:00:00Z"}`, "spent_at", 422},
		{"no direction", `{"adjustment_direction":null}`, "adjustment_direction", 422},
		{"negative", `{"amount":"-1"}`, "amount", 422},
		{"category", `{"category_uid":"cccccccc-0000-4000-8000-000000000001"}`, "category_uid", 422},
	} {
		if code, b := edit(c.changes, ifMatch(`"2"`)); code != c.code || fieldsOf(b)[c.field] == nil {
			t.Errorf("%s: %d %v", c.name, code, b)
		}
	}
	st, b := edit(`{"amount":"0","note":"new account","spent_at":"2026-09-30T00:00:00Z"}`, ifMatch(`"2"`))
	if st != 200 || b["amount"] != "0.00" || b["note"] != "new account" || b["type"] != "beginning_balance" || b["version"] != float64(3) {
		t.Fatalf("PUT: %d %v", st, b)
	}
	balanceIs("-100.00")
	if code, b := edit(`{"amount":"50","adjustment_direction":"decrease"}`, ifMatch(`"3"`)); code != 200 {
		t.Fatalf("PUT decrease: %d %v", code, b)
	}
	balanceIs("-150.00")

	// Never deleted on its own.
	if rec, b := do(t, h, "DELETE", path, ""); rec.Code != 409 || code(b) != "beginning_balance_undeletable" {
		t.Errorf("DELETE: %d %v", rec.Code, b)
	}
	if rec, _ := do(t, h, "GET", path, ""); rec.Code != 200 {
		t.Errorf("still there: %d", rec.Code)
	}
}
