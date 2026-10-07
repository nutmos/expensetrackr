package api

import (
	"net/http"
	"strconv"
	"testing"
)

func createBal(t *testing.T, h http.Handler, body string) (string, map[string]any) {
	t.Helper()
	rec, m := do(t, h, "POST", "/api/balances", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create balance: %d %s", rec.Code, rec.Body)
	}
	return "/api/balances/" + strconv.Itoa(int(m["id"].(float64))), m
}

func fieldsOf(body map[string]any) map[string]any {
	f, _ := body["fields"].(map[string]any)
	return f
}

func TestBalanceCreateGetListDelete(t *testing.T) {
	h := newTestServer(t)

	acctPath, acct := createBal(t, h, `{"name":"KBank debit","type":"payment_account","currency":"thb","balance":"-35.5","description":"salary account"}`)
	if acct["balance"] != "-35.50" || acct["balance_minor"].(float64) != -3550 || acct["kind"] != "asset" ||
		acct["debt"] != nil || acct["limit"] != nil || acct["available"] != nil || acct["over_limit"] != nil ||
		acct["currency"] != "THB" || acct["updated_at"] != nil {
		t.Errorf("asset response: %v", acct)
	}
	_, card := createBal(t, h, `{"name":"UOB One","type":"credit_card","currency":"SGD","debt":1200,"limit":"1000"}`)
	if card["debt"] != "1200.00" || card["limit"] != "1000.00" || card["available"] != "-200.00" || card["over_limit"] != true ||
		card["balance"] != nil || card["kind"] != "liability" {
		t.Errorf("liability response: %v", card)
	}
	createBal(t, h, `{"name":"Gold","type":"other_asset","currency":"THB","balance":"50000"}`)
	createBal(t, h, `{"name":"Car loan","type":"other_liability","currency":"THB","debt":"300000","limit":"500000"}`)

	rec, body := do(t, h, "GET", acctPath, "")
	if rec.Code != 200 || body["name"] != "KBank debit" || body["description"] != "salary account" {
		t.Errorf("get: %d %v", rec.Code, body)
	}

	rec, body = do(t, h, "GET", "/api/balances", "")
	if rec.Code != 200 || body["count"].(float64) != 4 {
		t.Fatalf("list: %d %v", rec.Code, body)
	}
	var order []string
	for _, it := range body["balances"].([]any) {
		order = append(order, it.(map[string]any)["type"].(string))
	}
	if want := "payment_account,credit_card,other_asset,other_liability"; join(order) != want {
		t.Errorf("list order %v, want %s", order, want)
	}
	totals := body["totals"].([]any)
	if len(totals) != 2 {
		t.Fatalf("totals: %v", totals)
	}
	sgd, thb := totals[0].(map[string]any), totals[1].(map[string]any)
	if sgd["currency"] != "SGD" || sgd["liabilities"] != "1200.00" || sgd["available_credit"] != "-200.00" {
		t.Errorf("SGD totals: %v", sgd)
	}
	if thb["assets"] != "49964.50" || thb["liabilities"] != "300000.00" || thb["net"] != "-250035.50" || thb["credit_limit"] != "500000.00" {
		t.Errorf("THB totals: %v", thb)
	}

	rec, body = do(t, h, "GET", "/api/balances?type=credit_card", "")
	if rec.Code != 200 || body["count"].(float64) != 1 {
		t.Errorf("type filter: %d %v", rec.Code, body)
	}
	rec, body = do(t, h, "GET", "/api/balances?type=wallet", "")
	if rec.Code != 400 || fieldsOf(body)["type"] == nil {
		t.Errorf("bad type filter: %d %v", rec.Code, body)
	}

	if rec, _ := do(t, h, "DELETE", acctPath, ""); rec.Code != http.StatusNoContent {
		t.Errorf("delete: %d", rec.Code)
	}
	if rec, body := do(t, h, "GET", acctPath, ""); rec.Code != 404 || body["error"] != "balance not found" {
		t.Errorf("get after delete: %d %v", rec.Code, body)
	}
	if rec, _ := do(t, h, "DELETE", acctPath, ""); rec.Code != 404 {
		t.Errorf("second delete: %d", rec.Code)
	}
}

func join(s []string) string {
	out := ""
	for i, v := range s {
		if i > 0 {
			out += ","
		}
		out += v
	}
	return out
}

func TestBalanceCreateErrors(t *testing.T) {
	h := newTestServer(t)
	createBal(t, h, `{"name":"Cash","type":"payment_account","currency":"THB","balance":"100"}`)
	cases := []struct {
		name, body string
		status     int
		fields     []string
	}{
		{"asset missing balance", `{"name":"A","type":"payment_account","currency":"THB"}`, 422, []string{"balance"}},
		{"asset with debt/limit", `{"name":"A","type":"other_asset","currency":"THB","balance":"1","debt":"1","limit":"2"}`, 422, []string{"debt", "limit"}},
		{"liability missing limit", `{"name":"C","type":"credit_card","currency":"THB","debt":"1"}`, 422, []string{"limit"}},
		{"liability with balance", `{"name":"C","type":"other_liability","currency":"THB","debt":"1","limit":"2","balance":"3"}`, 422, []string{"balance"}},
		{"negative debt", `{"name":"C","type":"credit_card","currency":"THB","debt":"-1","limit":"2"}`, 422, []string{"debt"}},
		{"bad type/currency/name", `{"name":"","type":"wallet","currency":"ABC"}`, 422, []string{"name", "type", "currency"}},
		{"too many decimals", `{"name":"J","type":"payment_account","currency":"JPY","balance":"1.5"}`, 422, []string{"balance"}},
		{"duplicate name (case-insensitive)", `{"name":"  cash ","type":"other_asset","currency":"USD","balance":"1"}`, 409, []string{"name"}},
		{"unknown field", `{"name":"Z","type":"payment_account","currency":"THB","balance":"1","color":"red"}`, 400, nil},
		{"malformed", `{"name":`, 400, nil},
		{"array", `[]`, 400, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec, body := do(t, h, "POST", "/api/balances", c.body)
			if rec.Code != c.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, c.status, rec.Body)
			}
			if body["error"] == nil {
				t.Errorf("missing error: %v", body)
			}
			for _, f := range c.fields {
				if fieldsOf(body)[f] == nil {
					t.Errorf("want field error %q, got %v", f, body)
				}
			}
		})
	}
}

func TestBalancePutAndPatch(t *testing.T) {
	h := newTestServer(t)
	path, orig := createBal(t, h, `{"name":"KBank","type":"payment_account","currency":"THB","balance":"100.50","description":"main"}`)
	createBal(t, h, `{"name":"Other","type":"other_asset","currency":"THB","balance":"1"}`)

	// PUT changing type must satisfy the new type: missing debt/limit -> 422.
	rec, body := do(t, h, "PUT", path, `{"name":"KBank","type":"credit_card","currency":"THB","balance":"100.50"}`)
	if rec.Code != 422 || fieldsOf(body)["debt"] == nil || fieldsOf(body)["limit"] == nil || fieldsOf(body)["balance"] == nil {
		t.Errorf("PUT type change w/o new fields: %d %v", rec.Code, body)
	}
	// Correct PUT type change.
	rec, body = do(t, h, "PUT", path, `{"name":"KBank Visa","type":"credit_card","currency":"THB","debt":"2500","limit":"50000"}`)
	if rec.Code != 200 || body["balance"] != nil || body["available"] != "47500.00" || body["description"] != "" ||
		body["id"] != orig["id"] || body["created_at"] != orig["created_at"] || body["updated_at"] == nil {
		t.Errorf("PUT type change: %d %v", rec.Code, body)
	}
	// PUT errors.
	if rec, _ := do(t, h, "PUT", "/api/balances/999", `{"name":"x","type":"payment_account","currency":"THB","balance":"1"}`); rec.Code != 404 {
		t.Errorf("PUT missing: %d", rec.Code)
	}
	if rec, _ := do(t, h, "PUT", "/api/balances/999", `{"name":""}`); rec.Code != 404 {
		t.Errorf("PUT missing + invalid: %d", rec.Code)
	}
	if rec, _ := do(t, h, "PUT", path, `{"name":"other","type":"payment_account","currency":"THB","balance":"1"}`); rec.Code != 409 {
		t.Errorf("PUT duplicate name: %d", rec.Code)
	}

	// PATCH partial.
	rec, body = do(t, h, "PATCH", path, `{"debt":"3000.75"}`)
	if rec.Code != 200 || body["debt"] != "3000.75" || body["limit"] != "50000.00" || body["name"] != "KBank Visa" {
		t.Errorf("PATCH debt: %d %v", rec.Code, body)
	}
	// PATCH type liability -> asset needs balance; then works with it.
	rec, body = do(t, h, "PATCH", path, `{"type":"payment_account"}`)
	if rec.Code != 422 || fieldsOf(body)["balance"] == nil {
		t.Errorf("PATCH type w/o balance: %d %v", rec.Code, body)
	}
	rec, body = do(t, h, "PATCH", path, `{"type":"payment_account","balance":"-12"}`)
	if rec.Code != 200 || body["balance"] != "-12.00" || body["debt"] != nil || body["limit"] != nil || body["over_limit"] != nil {
		t.Errorf("PATCH type with balance: %d %v", rec.Code, body)
	}
	// PATCH currency re-scale / rejection.
	rec, body = do(t, h, "PATCH", path, `{"currency":"KWD"}`)
	if rec.Code != 200 || body["balance"] != "-12.000" || body["balance_minor"].(float64) != -12000 {
		t.Errorf("PATCH KWD: %d %v", rec.Code, body)
	}
	do(t, h, "PATCH", path, `{"currency":"THB","balance":"-12.5"}`)
	rec, body = do(t, h, "PATCH", path, `{"currency":"JPY"}`)
	if rec.Code != 422 || fieldsOf(body)["balance"] == nil {
		t.Errorf("PATCH JPY with fractional balance: %d %v", rec.Code, body)
	}
	// PATCH errors.
	for _, c := range []struct {
		path, body string
		status     int
	}{
		{"/api/balances/999", `{"name":"x"}`, 404},
		{"/api/balances/abc", `{"name":"x"}`, 400},
		{path, `{}`, 400},
		{path, `null`, 400},
		{path, `{"colour":"red"}`, 400},
		{path, `{"name":5}`, 400},
		{path, `{"name":null}`, 422},
		{path, `{"name":"OTHER"}`, 409},
		{path, `{"debt":"5"}`, 422}, // asset now: debt not used
	} {
		if rec, body := do(t, h, "PATCH", c.path, c.body); rec.Code != c.status {
			t.Errorf("PATCH %s %s: %d, want %d (%v)", c.path, c.body, rec.Code, c.status, body)
		}
	}
	_, got := do(t, h, "GET", path, "")
	if got["balance"] != "-12.50" || got["currency"] != "THB" {
		t.Errorf("failed PATCHes changed the record: %v", got)
	}
}
