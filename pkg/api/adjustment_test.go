package api

import (
	"testing"
)

func TestBalanceAdjustmentAPI(t *testing.T) {
	h := newTestServer(t)
	cash := seedPayable(t, h, "Cash", "THB") // 10000.00
	card := seedCard(t, h, "Visa", "THB")    // debt 0, limit 50000
	list := func(query string) []any {
		t.Helper()
		rec, b := doH(t, h, "GET", "/api/transactions"+query, "", nil)
		if rec.Code != 200 {
			t.Fatalf("list %s: %d %v", query, rec.Code, b)
		}
		items, _ := b["transactions"].([]any)
		return items
	}

	// Manual edits record exactly one adjustment each; the response is the
	// balance as typed (the adjustment does not move it again).
	rec, b := doH(t, h, "PATCH", "/api/balances/"+cash, `{"balance":"9876.5"}`, ifMatch(`"1"`))
	if rec.Code != 200 || b["balance"] != "9876.50" || b["version"].(float64) != 2 {
		t.Fatalf("manual edit: %d %v", rec.Code, b)
	}
	rec, b = doH(t, h, "PUT", "/api/balances/"+card, `{"name":"Visa","type":"credit_card","currency":"THB","debt":"300","limit":"50000"}`, ifMatch(`"1"`))
	if rec.Code != 200 || b["debt"] != "300.00" {
		t.Fatalf("manual debt: %d %v", rec.Code, b)
	}
	// Limit-only and no-op edits record nothing.
	if rec, b := doH(t, h, "PATCH", "/api/balances/"+card, `{"limit":"60000"}`, ifMatch(`"2"`)); rec.Code != 200 {
		t.Fatalf("limit edit: %d %v", rec.Code, b)
	}
	if rec, b := doH(t, h, "PATCH", "/api/balances/"+cash, `{"balance":"9876.50","description":"main"}`, ifMatch(`"2"`)); rec.Code != 200 {
		t.Fatalf("no-op edit: %d %v", rec.Code, b)
	}
	adj := list("?type=balance_adjustment")
	if len(adj) != 2 {
		t.Fatalf("adjustments: %v", adj)
	}
	byBalance := map[string]map[string]any{}
	for _, a := range adj {
		m := a.(map[string]any)
		byBalance[m["balance_uid"].(string)] = m
	}
	if a := byBalance[cash]; a["type"] != "balance_adjustment" || a["amount"] != "123.50" || a["adjustment_direction"] != "decrease" ||
		a["currency"] != "THB" || a["account"] != "Cash" || a["category_uid"] != nil || a["to_balance_uid"] != nil || a["adjusts_balances"] != false {
		t.Errorf("cash adjustment: %v", a)
	}
	if a := byBalance[card]; a["amount"] != "300.00" || a["adjustment_direction"] != "increase" || a["note"] != "Manual edit of debt: 0.00 → 300.00" {
		t.Errorf("card adjustment: %v", a)
	}
	if _, b := doH(t, h, "GET", "/api/balances/"+cash, "", nil); b["balance"] != "9876.50" {
		t.Errorf("cash after edits: %v", b)
	}

	// An ordinary expense: listed with type expense; adjustments are in the
	// unfiltered list too; other filters exclude them.
	if rec, b := doH(t, h, "POST", "/api/transactions", `{"amount":"10","currency":"THB","balance_uid":"`+cash+`","spent_at":"2026-10-08T12:00:00+08:00"}`, nil); rec.Code != 201 {
		t.Fatalf("expense: %d %v", rec.Code, b)
	}
	if n := len(list("")); n != 3 {
		t.Errorf("all: %d", n)
	}
	if e := list("?type=expense"); len(e) != 1 {
		t.Errorf("expense filter: %v", e)
	}
	if rec, b := doH(t, h, "GET", "/api/transactions?type=adjustment", "", nil); rec.Code != 400 || fieldsOf(b)["type"] == nil {
		t.Errorf("bad filter: %d %v", rec.Code, b)
	}

	// The type cannot be created or chosen through the API: 422 on type.
	body := `{"type":"balance_adjustment","amount":"5","currency":"THB","balance_uid":"` + cash + `","spent_at":"2026-10-08T12:00:00+08:00"}`
	if rec, b := doH(t, h, "POST", "/api/transactions", body, nil); rec.Code != 422 || fieldsOf(b)["type"] == nil {
		t.Errorf("POST adjustment: %d %v", rec.Code, b)
	}
	exp := list("?type=expense")[0].(map[string]any)
	expPath := "/api/transactions/" + exp["uid"].(string)
	if rec, b := doH(t, h, "PUT", expPath, body, ifMatch(`"1"`)); rec.Code != 422 || fieldsOf(b)["type"] == nil {
		t.Errorf("PUT to adjustment: %d %v", rec.Code, b)
	}
	if rec, b := doH(t, h, "PATCH", expPath, `{"type":"balance_adjustment"}`, ifMatch(`"1"`)); rec.Code != 422 || fieldsOf(b)["type"] == nil {
		t.Errorf("PATCH to adjustment: %d %v", rec.Code, b)
	}
	if rec, b := doH(t, h, "PATCH", expPath, `{"adjustment_direction":"increase"}`, ifMatch(`"1"`)); rec.Code != 400 {
		t.Errorf("PATCH direction: %d %v", rec.Code, b)
	}

	// Existing adjustments are read-only: PUT/PATCH/DELETE -> 409, with or
	// without a version, and nothing changes.
	a := byBalance[cash]
	aPath := "/api/transactions/" + a["uid"].(string)
	for _, r := range []struct {
		method, body string
		hdr          map[string]string
	}{
		{"PUT", `{"type":"expense","amount":"1","currency":"THB","balance_uid":"` + cash + `","spent_at":"2026-10-08T12:00:00+08:00"}`, ifMatch(`"1"`)},
		{"PATCH", `{"note":"x"}`, ifMatch(`"1"`)},
		{"PATCH", `{"note":"x"}`, nil},
		{"DELETE", "", nil},
		{"DELETE", "", ifMatch(`"1"`)},
	} {
		rec, b := doH(t, h, r.method, aPath, r.body, r.hdr)
		if rec.Code != 409 || code(b) != "balance_adjustment_readonly" {
			t.Errorf("%s adjustment (%v): %d %v", r.method, r.hdr, rec.Code, b)
		}
	}
	rec, b = doH(t, h, "GET", aPath, "", nil)
	if rec.Code != 200 || b["version"].(float64) != 1 || b["amount"] != "123.50" || rec.Header().Get("ETag") != `"1"` {
		t.Errorf("adjustment after rejected writes: %d %v", rec.Code, b)
	}
	if _, b := doH(t, h, "GET", "/api/balances/"+cash, "", nil); b["balance"] != "9866.50" {
		t.Errorf("cash: %v", b)
	}
	// A referenced balance (here only by its adjustment) keeps its currency.
	if rec, b := doH(t, h, "PATCH", "/api/balances/"+card, `{"currency":"USD"}`, ifMatch(`"3"`)); rec.Code != 409 || code(b) != "balance_in_use" {
		t.Errorf("currency change: %d %v", rec.Code, b)
	}
}
