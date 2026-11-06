package api

import (
	"net/http"
	"testing"
)

func TestReturnOnInvestmentAPI(t *testing.T) {
	const roi = "return_on_investment"
	h := newTestServer(t)
	wallet := seedBal(t, h, "Wallet", "payment_account")
	card := seedBal(t, h, "Credit Card", "credit_card")
	gold := seedBal(t, h, "Gold", "other_asset")
	fund := seedBal(t, h, "Fund", "other_asset")
	loan := seedBal(t, h, "Loan", "other_liability")
	tx := func(extra string) string {
		return `{"type":"` + roi + `","amount":"10","currency":"THB","spent_at":"2026-10-07T10:00:00+08:00"` + extra + `}`
	}
	_, exp := do(t, h, http.MethodPost, "/api/categories", `{"name":"Food","type":"expense"}`)
	_, inc := do(t, h, http.MethodPost, "/api/categories", `{"name":"Dividends","type":"income"}`)
	expCat, _ := exp["uid"].(string)
	incCat, _ := inc["uid"].(string)
	if expCat == "" || incCat == "" {
		t.Fatalf("categories: %v %v", exp, inc)
	}

	cases := []struct{ name, body, field string }{
		{"no linked asset", tx(`,"balance_uid":"` + wallet + `"`), "to_balance_uid"},
		{"linked payment account", tx(`,"balance_uid":"` + card + `","to_balance_uid":"` + wallet + `"`), "to_balance_uid"},
		{"linked liability", tx(`,"balance_uid":"` + wallet + `","to_balance_uid":"` + loan + `"`), "to_balance_uid"},
		{"into other_asset", tx(`,"balance_uid":"` + fund + `","to_balance_uid":"` + gold + `"`), "balance_uid"},
		{"into liability", tx(`,"balance_uid":"` + loan + `","to_balance_uid":"` + gold + `"`), "balance_uid"},
		{"expense category", tx(`,"balance_uid":"` + wallet + `","to_balance_uid":"` + gold + `","category_uid":"` + expCat + `"`), "category_uid"},
		{"to_amount", tx(`,"balance_uid":"` + wallet + `","to_balance_uid":"` + gold + `","to_amount":"10"`), "to_amount"},
		{"currency mismatch", `{"type":"` + roi + `","amount":"10","currency":"USD","spent_at":"2026-10-07T10:00:00+08:00","balance_uid":"` + wallet + `","to_balance_uid":"` + gold + `"}`, "balance_uid"},
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

	rec, r := do(t, h, http.MethodPost, "/api/transactions", tx(`,"balance_uid":"`+wallet+`","to_balance_uid":"`+gold+`","category_uid":"`+incCat+`"`))
	if rec.Code != 201 || r["type"] != roi || r["account"] != "Wallet" || r["to_account"] != "Gold" || r["category_uid"] != incCat || r["adjusts_balances"] != true {
		t.Fatalf("create: %d %v", rec.Code, r)
	}
	if bal(wallet, "balance") != "10.00" || bal(gold, "balance") != "0.00" {
		t.Errorf("after create: wallet %v gold %v", bal(wallet, "balance"), bal(gold, "balance"))
	}
	if rec, b := do(t, h, http.MethodPost, "/api/transactions", tx(`,"balance_uid":"`+card+`","to_balance_uid":"`+fund+`"`)); rec.Code != 201 {
		t.Errorf("into card: %d %v", rec.Code, b)
	}
	if bal(card, "debt") != "-10.00" || bal(fund, "balance") != "0.00" {
		t.Errorf("after card return: card %v fund %v", bal(card, "debt"), bal(fund, "balance"))
	}
	_, l := do(t, h, http.MethodGet, "/api/transactions?type="+roi, "")
	if l["count"].(float64) != 2 {
		t.Errorf("filter count %v", l["count"])
	}

	id := r["uid"].(string)
	rec, p := putMerged(t, h, "/api/transactions/"+id, `{"amount":"4"}`)
	if rec.Code != 200 || bal(wallet, "balance") != "4.00" || bal(gold, "balance") != "0.00" {
		t.Errorf("edit: %d %v", rec.Code, p)
	}
	// return_on_investment -> income (link dropped): same effect.
	rec, p = putMerged(t, h, "/api/transactions/"+id, `{"type":"income","to_balance_uid":null}`)
	if rec.Code != 200 || p["to_balance_uid"] != nil || bal(wallet, "balance") != "4.00" {
		t.Errorf("to income: %d %v", rec.Code, p)
	}
	rec, p = putMerged(t, h, "/api/transactions/"+id, `{"type":"`+roi+`","to_balance_uid":"`+gold+`"}`)
	if rec.Code != 200 || p["to_account"] != "Gold" || bal(gold, "balance") != "0.00" {
		t.Errorf("back to roi: %d %v", rec.Code, p)
	}
	if rec, _ := do(t, h, http.MethodDelete, "/api/transactions/"+id, ""); rec.Code != 204 {
		t.Errorf("delete: %d", rec.Code)
	}
	if bal(wallet, "balance") != "0.00" || bal(gold, "balance") != "0.00" {
		t.Errorf("after delete: wallet %v gold %v", bal(wallet, "balance"), bal(gold, "balance"))
	}
}
