package api

import (
	"net/http"
	"testing"
)

func TestRepayDebtChargesAPI(t *testing.T) {
	h := newTestServer(t)
	wallet := seedBal(t, h, "Wallet", "payment_account")
	loan := seedBal(t, h, "Loan", "other_liability")
	_, c := do(t, h, http.MethodPost, "/api/categories", `{"name":"Loan interest","type":"expense"}`)
	expCat, _ := c["uid"].(string)
	_, c = do(t, h, http.MethodPost, "/api/categories", `{"name":"Salary","type":"income"}`)
	incCat, _ := c["uid"].(string)
	if expCat == "" || incCat == "" {
		t.Fatal("categories not created")
	}
	body := func(extra string) string {
		return `{"type":"repay_debt","amount":"100","currency":"THB","balance_uid":"` + wallet + `","to_balance_uid":"` + loan + `","spent_at":"2026-10-07T10:00:00+08:00"` + extra + `}`
	}
	bal := func(uid, key string) any {
		_, b := do(t, h, http.MethodGet, "/api/balances/"+uid, "")
		return b[key]
	}

	rec, r := do(t, h, http.MethodPost, "/api/transactions", body(`,"interest_amount":"12.5","fee_amount":3,"category_uid":"`+expCat+`"`))
	if rec.Code != http.StatusCreated || r["amount"] != "100.00" || r["interest_amount"] != "12.50" || r["interest_amount_minor"] != 1250.0 ||
		r["fee_amount"] != "3.00" || r["fee_amount_minor"] != 300.0 || r["category_uid"] != expCat {
		t.Fatalf("create: %d %v", rec.Code, r)
	}
	if bal(wallet, "balance") != "-115.50" || bal(loan, "debt") != "-100.00" {
		t.Errorf("after create: wallet %v loan %v, want -115.50 / -100.00", bal(wallet, "balance"), bal(loan, "debt"))
	}

	// The charges are expenses in the report; the principal is not.
	_, rep := do(t, h, http.MethodGet, "/api/reports/income-expense", "")
	curs, _ := rep["currencies"].([]any)
	if len(curs) != 1 {
		t.Fatalf("report: %v", rep)
	}
	thb := curs[0].(map[string]any)
	byCat, _ := thb["expense_by_category"].([]any)
	if thb["expense"] != "15.50" || thb["expense_count"] != 1.0 || len(byCat) != 1 || byCat[0].(map[string]any)["category_name"] != "Loan interest" {
		t.Errorf("report THB = %v", thb)
	}

	// PUT is a full replace: omitting the charges clears them (and then the
	// category must go too).
	uid := r["uid"].(string)
	rec, p := do(t, h, http.MethodPut, "/api/transactions/"+uid, body(`,"category_uid":"`+expCat+`"`))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("put category without charges: %d %v", rec.Code, p)
	}
	rec, p = do(t, h, http.MethodPut, "/api/transactions/"+uid, body(``))
	if rec.Code != http.StatusOK || p["interest_amount"] != nil || p["fee_amount"] != nil || p["category_uid"] != nil {
		t.Errorf("put without charges: %d %v", rec.Code, p)
	}
	if bal(wallet, "balance") != "-100.00" || bal(loan, "debt") != "-100.00" {
		t.Errorf("after clearing: wallet %v loan %v", bal(wallet, "balance"), bal(loan, "debt"))
	}

	cases := []struct{ name, body, field string }{
		{"income category", body(`,"interest_amount":"1","category_uid":"` + incCat + `"`), "category_uid"},
		{"negative fee", body(`,"fee_amount":"-1"`), "fee_amount"},
		{"interest on expense", `{"type":"expense","amount":"10","currency":"THB","balance_uid":"` + wallet + `","category_uid":"` + expCat + `","spent_at":"2026-10-07T10:00:00+08:00","interest_amount":"1"}`, "interest_amount"},
	}
	for _, c := range cases {
		rec, b := do(t, h, http.MethodPost, "/api/transactions", c.body)
		f, _ := b["fields"].(map[string]any)
		if rec.Code != http.StatusUnprocessableEntity || f[c.field] == nil {
			t.Errorf("%s: %d %v", c.name, rec.Code, b)
		}
	}

	// Bulk import carries the charges too.
	rec, b := do(t, h, http.MethodPost, "/api/transactions/bulk", bulkBody("",
		`{"type":"repay_debt","amount":"50","currency":"THB","balance_uid":"`+wallet+`","to_balance_uid":"`+loan+`","spent_at":"2026-10-08T10:00:00+08:00","interest_amount":"2"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("bulk: %d %s", rec.Code, rec.Body)
	}
	tx, _ := results(t, b)[0]["transaction"].(map[string]any)
	if tx["interest_amount"] != "2.00" || tx["fee_amount"] != nil {
		t.Errorf("bulk item: %v", tx)
	}
	if bal(wallet, "balance") != "-152.00" || bal(loan, "debt") != "-150.00" {
		t.Errorf("after bulk: wallet %v loan %v", bal(wallet, "balance"), bal(loan, "debt"))
	}
}
