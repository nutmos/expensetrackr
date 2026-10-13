package api

import (
	"net/http"
	"testing"
)

func TestAssetPurchaseAPI(t *testing.T) {
	h := newTestServer(t)
	pay := seedBal(t, h, "Bank", "payment_account")
	card := seedBal(t, h, "Card", "credit_card")
	gold := seedBal(t, h, "Gold", "other_asset")
	loan := seedBal(t, h, "Loan", "other_liability")
	gold2 := seedBal(t, h, "Watch", "other_asset")
	tx := func(extra string) string {
		return `{"type":"asset_purchase","amount":"10","currency":"THB","spent_at":"2026-10-07T10:00:00+08:00"` + extra + `}`
	}
	_, exp := do(t, h, http.MethodPost, "/api/categories", `{"name":"Electronics","type":"expense"}`)
	_, inc := do(t, h, http.MethodPost, "/api/categories", `{"name":"Salary","type":"income"}`)
	expCat, _ := exp["uid"].(string)
	incCat, _ := inc["uid"].(string)
	if expCat == "" || incCat == "" {
		t.Fatalf("categories: %v %v", exp, inc)
	}

	cases := []struct{ name, body, field string }{
		{"no dest", tx(`,"balance_uid":"` + pay + `"`), "to_balance_uid"},
		{"dest payment account", tx(`,"balance_uid":"` + pay + `","to_balance_uid":"` + card + `"`), "to_balance_uid"},
		{"dest liability", tx(`,"balance_uid":"` + pay + `","to_balance_uid":"` + loan + `"`), "to_balance_uid"},
		{"same balance", tx(`,"balance_uid":"` + gold + `","to_balance_uid":"` + gold + `"`), "to_balance_uid"},
		{"source other_asset", tx(`,"balance_uid":"` + gold + `","to_balance_uid":"` + gold2 + `"`), "balance_uid"},
		{"source liability", tx(`,"balance_uid":"` + loan + `","to_balance_uid":"` + gold + `"`), "balance_uid"},
		{"income category", tx(`,"balance_uid":"` + pay + `","to_balance_uid":"` + gold + `","category_uid":"` + incCat + `"`), "category_uid"},
		{"currency mismatch", `{"type":"asset_purchase","amount":"10","currency":"USD","spent_at":"2026-10-07T10:00:00+08:00","balance_uid":"` + pay + `","to_balance_uid":"` + gold + `"}`, "balance_uid"},
	}
	for _, c := range cases {
		rec, b := do(t, h, http.MethodPost, "/api/transactions", c.body)
		f, _ := b["fields"].(map[string]any)
		if rec.Code != 422 || f[c.field] == nil {
			t.Errorf("%s: %d %v", c.name, rec.Code, b)
		}
	}
	bal := func(uid, key string) any {
		_, b := do(t, h, http.MethodGet, "/api/balances/"+uid, "")
		return b[key]
	}

	rec, ap := do(t, h, http.MethodPost, "/api/transactions", tx(`,"balance_uid":"`+card+`","to_balance_uid":"`+gold+`","category_uid":"`+expCat+`"`))
	if rec.Code != 201 || ap["type"] != "asset_purchase" || ap["account"] != "Card" || ap["to_account"] != "Gold" || ap["category_uid"] != expCat {
		t.Fatalf("create: %d %v", rec.Code, ap)
	}
	if bal(card, "debt") != "10.00" || bal(gold, "balance") != "10.00" {
		t.Errorf("after create: card %v gold %v", bal(card, "debt"), bal(gold, "balance"))
	}
	if rec, b := do(t, h, http.MethodPost, "/api/transactions", tx(`,"balance_uid":"`+pay+`","to_balance_uid":"`+gold+`"`)); rec.Code != 201 {
		t.Errorf("from bank: %d %v", rec.Code, b)
	}
	if bal(pay, "balance") != "-10.00" || bal(gold, "balance") != "20.00" {
		t.Errorf("after bank purchase: %v %v", bal(pay, "balance"), bal(gold, "balance"))
	}
	_, l := do(t, h, http.MethodGet, "/api/transactions?type=asset_purchase", "")
	if l["count"].(float64) != 2 {
		t.Errorf("filter count %v", l["count"])
	}
	id := ap["uid"].(string)
	rec, p := putMerged(t, h, "/api/transactions/"+id, `{"amount":"4"}`)
	if rec.Code != 200 || bal(card, "debt") != "4.00" || bal(gold, "balance") != "14.00" {
		t.Errorf("edit: %d %v card %v gold %v", rec.Code, p, bal(card, "debt"), bal(gold, "balance"))
	}
	// asset_purchase -> expense (destination dropped).
	rec, p = putMerged(t, h, "/api/transactions/"+id, `{"type":"expense","to_balance_uid":null}`)
	if rec.Code != 200 || p["to_balance_uid"] != nil || bal(card, "debt") != "4.00" || bal(gold, "balance") != "10.00" {
		t.Errorf("to expense: %d %v", rec.Code, p)
	}
	rec, p = putMerged(t, h, "/api/transactions/"+id, `{"type":"asset_purchase","to_balance_uid":"`+gold+`"}`)
	if rec.Code != 200 || bal(gold, "balance") != "14.00" {
		t.Errorf("back to asset_purchase: %d %v", rec.Code, p)
	}
	if rec, _ := do(t, h, http.MethodDelete, "/api/transactions/"+id, ""); rec.Code != 204 {
		t.Errorf("delete: %d", rec.Code)
	}
	if bal(card, "debt") != "0.00" || bal(gold, "balance") != "10.00" {
		t.Errorf("after delete: card %v gold %v", bal(card, "debt"), bal(gold, "balance"))
	}
}
