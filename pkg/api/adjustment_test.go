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
	rec, b := putMergedH(t, h, "/api/balances/"+cash, `{"balance":"9876.5"}`, ifMatch(`"1"`))
	if rec.Code != 200 || b["balance"] != "9876.50" || b["version"].(float64) != 2 {
		t.Fatalf("manual edit: %d %v", rec.Code, b)
	}
	rec, b = doH(t, h, "PUT", "/api/balances/"+card, `{"name":"Visa","type":"credit_card","currency":"THB","debt":"300","limit":"50000"}`, ifMatch(`"1"`))
	if rec.Code != 200 || b["debt"] != "300.00" {
		t.Fatalf("manual debt: %d %v", rec.Code, b)
	}
	// Limit-only and no-op edits record nothing.
	if rec, b := putMergedH(t, h, "/api/balances/"+card, `{"limit":"60000"}`, ifMatch(`"2"`)); rec.Code != 200 {
		t.Fatalf("limit edit: %d %v", rec.Code, b)
	}
	if rec, b := putMergedH(t, h, "/api/balances/"+cash, `{"balance":"9876.50","description":"main"}`, ifMatch(`"2"`)); rec.Code != 200 {
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
		a["currency"] != "THB" || a["account"] != "Cash" || a["category_uid"] != nil || a["to_balance_uid"] != nil || a["adjusts_balances"] != true {
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
	if rec, b := do(t, h, "POST", "/api/transactions", `{"amount":"10","currency":"THB","balance_uid":"`+cash+`","spent_at":"2026-10-08T12:00:00+08:00"}`); rec.Code != 201 {
		t.Fatalf("expense: %d %v", rec.Code, b)
	}
	if n := len(list("")); n != 5 { // + the two balances' beginning balances
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
	if rec, b := putMergedH(t, h, expPath, `{"type":"balance_adjustment"}`, ifMatch(`"1"`)); rec.Code != 422 || fieldsOf(b)["type"] == nil {
		t.Errorf("PUT to adjustment: %d %v", rec.Code, b)
	}
	if rec, b := putMergedH(t, h, expPath, `{"adjustment_direction":"increase"}`, ifMatch(`"1"`)); rec.Code != 422 || fieldsOf(b)["adjustment_direction"] == nil {
		t.Errorf("PUT direction: %d %v", rec.Code, b)
	}

	// Existing adjustments can be edited (they stay adjustments) and deleted;
	// the balance moves by the difference. Cash: 9876.50 after the edit,
	// 9866.50 after the expense; its adjustment is a decrease of 123.50.
	a := byBalance[cash]
	aPath := "/api/transactions/" + a["uid"].(string)
	adjBody := func(fields string) string {
		return `{"amount":"100","currency":"THB","balance_uid":"` + cash + `","spent_at":"2026-10-08T12:00:00+08:00","note":"recount"` + fields + `}`
	}
	cashIs := func(want string) {
		t.Helper()
		if _, b := doH(t, h, "GET", "/api/balances/"+cash, "", nil); b["balance"] != want {
			t.Errorf("cash = %v, want %s", b["balance"], want)
		}
	}
	for _, r := range []struct {
		name, body string
		hdr        map[string]string
		code       int
		field      string
	}{
		{"other type", adjBody(`,"type":"expense","adjustment_direction":"decrease"`), ifMatch(`"1"`), 422, "type"},
		{"no direction", adjBody(``), ifMatch(`"1"`), 422, "adjustment_direction"},
		{"bad direction", adjBody(`,"adjustment_direction":"up"`), ifMatch(`"1"`), 422, "adjustment_direction"},
		{"category", adjBody(`,"adjustment_direction":"decrease","category_uid":"cccccccc-0000-4000-8000-000000000001"`), ifMatch(`"1"`), 422, "category_uid"},
		{"no version", adjBody(`,"adjustment_direction":"decrease"`), nil, 428, ""},
		{"stale", adjBody(`,"adjustment_direction":"decrease"`), ifMatch(`"7"`), 409, ""},
		{"missing balance", `{"amount":"1","currency":"THB","balance_uid":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","spent_at":"2026-10-08T12:00:00+08:00","adjustment_direction":"increase"}`, ifMatch(`"1"`), 422, "balance_uid"},
		{"currency mismatch", `{"amount":"1","currency":"USD","balance_uid":"` + cash + `","spent_at":"2026-10-08T12:00:00+08:00","adjustment_direction":"increase"}`, ifMatch(`"1"`), 422, "balance_uid"},
	} {
		rec, b := doH(t, h, "PUT", aPath, r.body, r.hdr)
		if rec.Code != r.code || (r.field != "" && fieldsOf(b)[r.field] == nil) {
			t.Errorf("PUT adjustment (%s): %d %v", r.name, rec.Code, b)
		}
	}
	cashIs("9866.50")
	// decrease 123.50 -> decrease 100.00: cash +23.50.
	rec, b = doH(t, h, "PUT", aPath, adjBody(`,"type":"balance_adjustment","adjustment_direction":"decrease"`), ifMatch(`"1"`))
	if rec.Code != 200 || b["amount"] != "100.00" || b["type"] != "balance_adjustment" || b["adjustment_direction"] != "decrease" ||
		b["note"] != "recount" || b["version"].(float64) != 2 || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("PUT adjustment: %d %v", rec.Code, b)
	}
	cashIs("9890.00")
	// Round-tripping the response with only the direction changed: +200.
	rec, b = putMergedH(t, h, aPath, `{"adjustment_direction":"increase"}`, ifMatch(`"2"`))
	if rec.Code != 200 || b["adjustment_direction"] != "increase" {
		t.Fatalf("merged PUT adjustment: %d %v", rec.Code, b)
	}
	cashIs("10090.00")
	// Moved to the card: cash loses the increase, the card's debt rises.
	rec, b = putMergedH(t, h, aPath, `{"balance_uid":"`+card+`"}`, ifMatch(`"3"`))
	if rec.Code != 200 || b["account"] != "Visa" {
		t.Fatalf("move adjustment: %d %v", rec.Code, b)
	}
	cashIs("9990.00")
	if _, b := doH(t, h, "GET", "/api/balances/"+card, "", nil); b["debt"] != "400.00" {
		t.Errorf("card after move: %v", b)
	}
	// Deleting it reverts it (with or without If-Match; a stale one is 409).
	if rec, b := doH(t, h, "DELETE", aPath, "", ifMatch(`"1"`)); rec.Code != 409 {
		t.Errorf("stale DELETE: %d %v", rec.Code, b)
	}
	if rec, b := doH(t, h, "DELETE", aPath, "", ifMatch(`"4"`)); rec.Code != 204 {
		t.Fatalf("DELETE adjustment: %d %v", rec.Code, b)
	}
	if rec, _ := doH(t, h, "GET", aPath, "", nil); rec.Code != 404 {
		t.Errorf("deleted adjustment: %d", rec.Code)
	}
	if _, b := doH(t, h, "GET", "/api/balances/"+card, "", nil); b["debt"] != "300.00" {
		t.Errorf("card after delete: %v", b)
	}
	cashIs("9990.00")
	// A referenced balance (here only by its adjustment) keeps its currency.
	if rec, b := putMergedH(t, h, "/api/balances/"+card, `{"currency":"USD"}`, ifMatch(`"5"`)); rec.Code != 409 || code(b) != "balance_in_use" {
		t.Errorf("currency change: %d %v", rec.Code, b)
	}
}
