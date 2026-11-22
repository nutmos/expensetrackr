package api

import (
	"net/http"
	"testing"
)

func TestAssetSaleAPI(t *testing.T) {
	h := newTestServer(t)
	wallet := seedBal(t, h, "Wallet", "payment_account")
	gold := seedBal(t, h, "Gold", "other_asset")
	_, c := do(t, h, http.MethodPost, "/api/categories", `{"name":"Investment loss","type":"expense"}`)
	expCat, _ := c["uid"].(string)
	_, c = do(t, h, http.MethodPost, "/api/categories", `{"name":"Capital gains","type":"income"}`)
	incCat, _ := c["uid"].(string)
	if expCat == "" || incCat == "" {
		t.Fatal("categories not created")
	}
	body := func(extra string) string {
		return `{"type":"asset_sale","amount":"100","currency":"THB","balance_uid":"` + wallet + `","to_balance_uid":"` + gold + `","spent_at":"2026-10-07T10:00:00+08:00"` + extra + `}`
	}
	bal := func(uid, key string) any {
		_, b := do(t, h, http.MethodGet, "/api/balances/"+uid, "")
		return b[key]
	}

	rec, r := do(t, h, http.MethodPost, "/api/transactions", body(`,"gain_loss":"25.5","category_uid":"`+incCat+`"`))
	if rec.Code != http.StatusCreated || r["amount"] != "100.00" || r["gain_loss"] != "25.50" || r["gain_loss_minor"] != 2550.0 ||
		r["entire_asset_sale"] != false || r["category_uid"] != incCat || r["to_account"] != "Gold" {
		t.Fatalf("create: %d %v", rec.Code, r)
	}
	if bal(wallet, "balance") != "125.50" || bal(gold, "balance") != "-100.00" || bal(gold, "hidden") != false {
		t.Errorf("after create: wallet %v gold %v", bal(wallet, "balance"), bal(gold, "balance"))
	}

	// The gain is income in the report; the principal is not.
	_, rep := do(t, h, http.MethodGet, "/api/reports/income-expense", "")
	curs, _ := rep["currencies"].([]any)
	if len(curs) != 1 {
		t.Fatalf("report: %v", rep)
	}
	thb := curs[0].(map[string]any)
	byCat, _ := thb["income_by_category"].([]any)
	if thb["income"] != "25.50" || thb["expense"] != "0.00" || len(byCat) != 1 || byCat[0].(map[string]any)["category_name"] != "Capital gains" {
		t.Errorf("report THB = %v", thb)
	}

	// PUT as a loss of the entire asset: the category must be an expense
	// category now, and the asset is hidden.
	uid := r["uid"].(string)
	rec, p := do(t, h, http.MethodPut, "/api/transactions/"+uid, body(`,"version":1,"gain_loss":"-40","category_uid":"`+incCat+`","entire_asset_sale":true`))
	if f, _ := p["fields"].(map[string]any); rec.Code != http.StatusUnprocessableEntity || f["category_uid"] == nil {
		t.Errorf("put loss with income category: %d %v", rec.Code, p)
	}
	rec, p = do(t, h, http.MethodPut, "/api/transactions/"+uid, body(`,"version":1,"gain_loss":"-40","category_uid":"`+expCat+`","entire_asset_sale":true`))
	if rec.Code != http.StatusOK || p["gain_loss"] != "-40.00" || p["entire_asset_sale"] != true {
		t.Fatalf("put loss: %d %v", rec.Code, p)
	}
	if bal(wallet, "balance") != "60.00" || bal(gold, "balance") != "-100.00" || bal(gold, "hidden") != true {
		t.Errorf("after loss: wallet %v gold %v hidden %v", bal(wallet, "balance"), bal(gold, "balance"), bal(gold, "hidden"))
	}

	cases := []struct{ name, body, field string }{
		{"loss above principal", body(`,"gain_loss":"-100.01"`), "gain_loss"},
		{"category without gain/loss", body(`,"category_uid":"` + expCat + `"`), "category_uid"},
		{"asset is not an other_asset", `{"type":"asset_sale","amount":"10","currency":"THB","balance_uid":"` + gold + `","to_balance_uid":"` + wallet + `","spent_at":"2026-10-07T10:00:00+08:00"}`, "balance_uid"},
		{"gain on expense", `{"type":"expense","amount":"10","currency":"THB","balance_uid":"` + wallet + `","category_uid":"` + expCat + `","spent_at":"2026-10-07T10:00:00+08:00","gain_loss":"1"}`, "gain_loss"},
	}
	for _, c := range cases {
		rec, b := do(t, h, http.MethodPost, "/api/transactions", c.body)
		f, _ := b["fields"].(map[string]any)
		if rec.Code != http.StatusUnprocessableEntity || f[c.field] == nil {
			t.Errorf("%s: %d %v", c.name, rec.Code, b)
		}
	}

	// Bulk import carries the sale fields too.
	rec, b := do(t, h, http.MethodPost, "/api/transactions/bulk", bulkBody("",
		`{"type":"asset_sale","amount":"50","currency":"THB","balance_uid":"`+wallet+`","to_balance_uid":"`+gold+`","spent_at":"2026-10-08T10:00:00+08:00","gain_loss":"-5"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("bulk: %d %s", rec.Code, rec.Body)
	}
	tx, _ := results(t, b)[0]["transaction"].(map[string]any)
	if tx["gain_loss"] != "-5.00" || tx["entire_asset_sale"] != false {
		t.Errorf("bulk item: %v", tx)
	}
	if bal(wallet, "balance") != "105.00" || bal(gold, "balance") != "-150.00" {
		t.Errorf("after bulk: wallet %v gold %v", bal(wallet, "balance"), bal(gold, "balance"))
	}
}

// TestAssetSaleReceiverTypes: the proceeds can be received into a
// payment_account or credit_card only, never another asset or a liability.
func TestAssetSaleReceiverTypes(t *testing.T) {
	h := newTestServer(t)
	gold := seedBal(t, h, "Gold", "other_asset")
	for _, typ := range []string{"payment_account", "credit_card", "other_asset", "other_liability"} {
		recv := seedBal(t, h, "Receiver "+typ, typ)
		rec, b := do(t, h, http.MethodPost, "/api/transactions", `{"type":"asset_sale","amount":"10","currency":"THB","balance_uid":"`+recv+`","to_balance_uid":"`+gold+`","spent_at":"2026-10-07T10:00:00+08:00"}`)
		ok := typ == "payment_account" || typ == "credit_card"
		if f, _ := b["fields"].(map[string]any); ok && rec.Code != http.StatusCreated || !ok && (rec.Code != http.StatusUnprocessableEntity || f["balance_uid"] == nil) {
			t.Errorf("receiver %s: %d %v", typ, rec.Code, b)
		}
	}
}
