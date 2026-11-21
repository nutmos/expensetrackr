package api

import (
	"net/http"
	"testing"
)

// TestBalanceHide: hidden balances leave the list (and its totals) unless
// include_hidden is set, stay readable and usable, and come back on unhide.
func TestBalanceHide(t *testing.T) {
	h := newTestServer(t)
	_, cash := createBal(t, h, `{"name":"Cash","type":"payment_account","currency":"THB","balance":"100"}`)
	oldPath, old := createBal(t, h, `{"name":"Old account","type":"payment_account","currency":"THB","balance":"40"}`)
	if cash["hidden"] != false {
		t.Errorf("new balance hidden = %v", cash["hidden"])
	}

	rec, b := doH(t, h, "POST", oldPath+"/hide", "", nil)
	if rec.Code != 200 || b["hidden"] != true || b["version"] != float64(2) || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("hide: %d %v", rec.Code, b)
	}
	// Again: no change.
	if rec, b := doH(t, h, "POST", oldPath+"/hide", "", nil); rec.Code != 200 || b["version"] != float64(2) {
		t.Errorf("hide again: %d %v", rec.Code, b)
	}
	// Stale If-Match: 409 with the current balance.
	if rec, b := doH(t, h, "POST", oldPath+"/unhide", "", ifMatch(`"1"`)); rec.Code != 409 || code(b) != "version_conflict" {
		t.Errorf("stale unhide: %d %v", rec.Code, b)
	}

	rec, list := do(t, h, "GET", "/api/balances", "")
	if rec.Code != 200 || list["count"] != float64(1) || list["balances"].([]any)[0].(map[string]any)["uid"] != cash["uid"] {
		t.Fatalf("list without hidden: %d %v", rec.Code, list)
	}
	if thb := list["totals"].([]any)[0].(map[string]any); thb["assets"] != "100.00" {
		t.Errorf("totals without hidden: %v", thb)
	}
	for _, q := range []string{"?include_hidden=true", "?include_hidden=1", "?payable=1&include_hidden=TRUE", "?type=payment_account&include_hidden=true"} {
		if rec, list := do(t, h, "GET", "/api/balances"+q, ""); rec.Code != 200 || list["count"] != float64(2) {
			t.Errorf("list %s: %d %v", q, rec.Code, list)
		}
	}
	if rec, list := do(t, h, "GET", "/api/balances?payable=1", ""); rec.Code != 200 || list["count"] != float64(1) {
		t.Errorf("payable without hidden: %d %v", rec.Code, list)
	}

	// Still readable and usable by transactions.
	if rec, b := do(t, h, "GET", oldPath, ""); rec.Code != 200 || b["hidden"] != true {
		t.Errorf("get hidden: %d %v", rec.Code, b)
	}
	if rec, b := do(t, h, "POST", "/api/transactions", `{"amount":"10","currency":"THB","balance_uid":"`+old["uid"].(string)+`","spent_at":"2026-10-08T12:00:00+08:00"}`); rec.Code != 201 {
		t.Fatalf("expense on hidden: %d %v", rec.Code, b)
	}
	// "hidden" is not a PUT field: hiding has its own endpoints.
	if rec, b := doH(t, h, "PUT", oldPath, `{"name":"Old","type":"payment_account","currency":"THB","balance":"30","hidden":false}`, ifMatch(`"3"`)); rec.Code != 400 {
		t.Errorf("PUT with hidden: %d %v", rec.Code, b)
	}
	if rec, b := doH(t, h, "PUT", oldPath, `{"name":"Old","type":"payment_account","currency":"THB","balance":"30"}`, ifMatch(`"3"`)); rec.Code != 200 || b["hidden"] != true {
		t.Errorf("PUT keeps hidden: %d %v", rec.Code, b)
	}

	rec, b = doH(t, h, "POST", oldPath+"/unhide", "", ifMatch(`"4"`))
	if rec.Code != 200 || b["hidden"] != false || b["version"] != float64(5) {
		t.Errorf("unhide: %d %v", rec.Code, b)
	}
	if rec, list := do(t, h, "GET", "/api/balances", ""); rec.Code != 200 || list["count"] != float64(2) {
		t.Errorf("list after unhide: %d %v", rec.Code, list)
	}

	if rec, _ := doH(t, h, "POST", "/api/balances/abc/hide", "", nil); rec.Code != 400 {
		t.Errorf("hide bad uid: %d", rec.Code)
	}
	if rec, _ := doH(t, h, "POST", "/api/balances/11111111-1111-4111-8111-111111111111/hide", "", nil); rec.Code != 404 {
		t.Errorf("hide unknown: %d", rec.Code)
	}
}

// TestBalanceDeleteRemovesTransactions: DELETE removes the balance and every
// transaction on it, and puts back what they did to other balances.
func TestBalanceDeleteRemovesTransactions(t *testing.T) {
	h := newTestServer(t)
	_, cash := createBal(t, h, `{"name":"Cash","type":"payment_account","currency":"THB","balance":"100"}`)
	savingsPath, savings := createBal(t, h, `{"name":"Savings","type":"payment_account","currency":"THB","balance":"0"}`)
	cashUID, savingsUID := cash["uid"].(string), savings["uid"].(string)
	post := func(body string) {
		t.Helper()
		if rec, b := do(t, h, "POST", "/api/transactions", body); rec.Code != http.StatusCreated {
			t.Fatalf("create: %d %v", rec.Code, b)
		}
	}
	post(`{"amount":"5","currency":"THB","balance_uid":"` + cashUID + `","spent_at":"2026-10-08T12:00:00+08:00"}`)                                                          // cash 95
	post(`{"type":"transfer","amount":"30","currency":"THB","balance_uid":"` + cashUID + `","to_balance_uid":"` + savingsUID + `","spent_at":"2026-10-08T13:00:00+08:00"}`) // cash 65, savings 30
	post(`{"amount":"2","currency":"THB","balance_uid":"` + savingsUID + `","spent_at":"2026-10-08T14:00:00+08:00"}`)                                                       // savings 28

	if rec, b := doH(t, h, "DELETE", savingsPath, "", ifMatch(`"1"`)); rec.Code != 409 || code(b) != "version_conflict" {
		t.Errorf("stale delete: %d %v", rec.Code, b)
	}
	rec, b := do(t, h, "DELETE", savingsPath, "")
	if rec.Code != 200 || b["deleted_transactions"] != float64(2) {
		t.Fatalf("delete: %d %v", rec.Code, b)
	}
	if rec, b := do(t, h, "GET", "/api/balances/"+cashUID, ""); rec.Code != 200 || b["balance"] != "95.00" {
		t.Errorf("cash after delete: %d %v", rec.Code, b)
	}
	rec, list := do(t, h, "GET", "/api/transactions", "")
	if rec.Code != 200 || list["count"] != float64(1) {
		t.Fatalf("transactions left: %d %v", rec.Code, list)
	}
	if e := list["transactions"].([]any)[0].(map[string]any); e["balance_uid"] != cashUID || e["to_balance_uid"] != nil {
		t.Errorf("left: %v", e)
	}
}
