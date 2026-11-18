package api

import (
	"net/http"
	"testing"
)

func TestRepayDebtAPI(t *testing.T) {
	const repay = "repay_debt"
	h := newTestServer(t)
	wallet := seedBal(t, h, "Wallet", "payment_account")
	card := seedBal(t, h, "Credit Card", "credit_card")
	gold := seedBal(t, h, "Gold", "other_asset")
	loan := seedBal(t, h, "Loan", "other_liability")
	tx := func(extra string) string {
		return `{"type":"` + repay + `","amount":"10","currency":"THB","spent_at":"2026-10-07T10:00:00+08:00"` + extra + `}`
	}
	_, exp := do(t, h, http.MethodPost, "/api/categories", `{"name":"Food","type":"expense"}`)
	expCat, _ := exp["uid"].(string)
	if expCat == "" {
		t.Fatalf("category: %v", exp)
	}

	cases := []struct{ name, body, field string }{
		{"no debt", tx(`,"balance_uid":"` + wallet + `"`), "to_balance_uid"},
		{"to payment account", tx(`,"balance_uid":"` + wallet + `","to_balance_uid":"` + wallet + `"`), "to_balance_uid"},
		{"to other_asset", tx(`,"balance_uid":"` + wallet + `","to_balance_uid":"` + gold + `"`), "to_balance_uid"},
		{"from credit card", tx(`,"balance_uid":"` + card + `","to_balance_uid":"` + loan + `"`), "balance_uid"},
		{"from other_asset", tx(`,"balance_uid":"` + gold + `","to_balance_uid":"` + loan + `"`), "balance_uid"},
		{"with category", tx(`,"balance_uid":"` + wallet + `","to_balance_uid":"` + loan + `","category_uid":"` + expCat + `"`), "category_uid"},
		{"to_amount", tx(`,"balance_uid":"` + wallet + `","to_balance_uid":"` + loan + `","to_amount":"10"`), "to_amount"},
		{"currency mismatch", `{"type":"` + repay + `","amount":"10","currency":"USD","spent_at":"2026-10-07T10:00:00+08:00","balance_uid":"` + wallet + `","to_balance_uid":"` + loan + `"}`, "balance_uid"},
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

	rec, r := do(t, h, http.MethodPost, "/api/transactions", tx(`,"balance_uid":"`+wallet+`","to_balance_uid":"`+loan+`"`))
	if rec.Code != 201 || r["type"] != repay || r["account"] != "Wallet" || r["to_account"] != "Loan" || r["category_uid"] != nil || r["adjusts_balances"] != true {
		t.Fatalf("create: %d %v", rec.Code, r)
	}
	if bal(wallet, "balance") != "-10.00" || bal(loan, "debt") != "-10.00" {
		t.Errorf("after create: wallet %v loan %v", bal(wallet, "balance"), bal(loan, "debt"))
	}
	if rec, b := do(t, h, http.MethodPost, "/api/transactions", tx(`,"balance_uid":"`+wallet+`","to_balance_uid":"`+card+`"`)); rec.Code != 201 {
		t.Errorf("pay card: %d %v", rec.Code, b)
	}
	if bal(card, "debt") != "-10.00" || bal(wallet, "balance") != "-20.00" {
		t.Errorf("after card payment: card %v wallet %v", bal(card, "debt"), bal(wallet, "balance"))
	}
	_, l := do(t, h, http.MethodGet, "/api/transactions?type="+repay, "")
	if l["count"].(float64) != 2 {
		t.Errorf("filter count %v", l["count"])
	}

	id := r["uid"].(string)
	rec, p := putMerged(t, h, "/api/transactions/"+id, `{"amount":"4"}`)
	if rec.Code != 200 || bal(wallet, "balance") != "-14.00" || bal(loan, "debt") != "-4.00" {
		t.Errorf("edit: %d %v", rec.Code, p)
	}
	// repay_debt -> transfer: same effect.
	rec, p = putMerged(t, h, "/api/transactions/"+id, `{"type":"transfer"}`)
	if rec.Code != 200 || p["type"] != "transfer" || bal(loan, "debt") != "-4.00" {
		t.Errorf("to transfer: %d %v", rec.Code, p)
	}
	rec, p = putMerged(t, h, "/api/transactions/"+id, `{"type":"`+repay+`"}`)
	if rec.Code != 200 || p["type"] != repay || p["to_account"] != "Loan" {
		t.Errorf("back to repay_debt: %d %v", rec.Code, p)
	}
	if rec, _ := do(t, h, http.MethodDelete, "/api/transactions/"+id, ""); rec.Code != 204 {
		t.Errorf("delete: %d", rec.Code)
	}
	if bal(wallet, "balance") != "-10.00" || bal(loan, "debt") != "0.00" {
		t.Errorf("after delete: wallet %v loan %v", bal(wallet, "balance"), bal(loan, "debt"))
	}
}
