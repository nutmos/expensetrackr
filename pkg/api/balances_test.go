package api

import (
	"net/http"
	"testing"
)

func createBal(t *testing.T, h http.Handler, body string) (string, map[string]any) {
	t.Helper()
	rec, m := do(t, h, "POST", "/api/balances", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create balance: %d %s", rec.Code, rec.Body)
	}
	return "/api/balances/" + m["uid"].(string), m
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
		acct["currency"] != "THB" || acct["updated_at"] != nil || !looksLikeUID(acct["uid"]) {
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
		{"negative limit", `{"name":"C","type":"credit_card","currency":"THB","debt":"-1","limit":"-2"}`, 422, []string{"limit"}}, // negative debt (in credit) is fine
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

	// The type is immutable: PUT with another type is 422 on "type" (even when
	// the amounts would suit the new type), and nothing changes.
	for _, b := range []string{
		`{"name":"KBank","type":"credit_card","currency":"THB","balance":"100.50"}`,
		`{"name":"KBank Visa","type":"credit_card","currency":"THB","debt":"2500","limit":"50000"}`,
		`{"name":"KBank","type":"other_asset","currency":"THB","balance":"1"}`,
	} {
		rec, body := do(t, h, "PUT", path, b)
		if rec.Code != 422 || fieldsOf(body)["type"] != "balance type cannot be changed after creation" {
			t.Errorf("PUT type change %s: %d %v", b, rec.Code, body)
		}
	}
	// PUT still requires type.
	if rec, body := do(t, h, "PUT", path, `{"name":"KBank","currency":"THB","balance":"1"}`); rec.Code != 422 || fieldsOf(body)["type"] == nil {
		t.Errorf("PUT without type: %d %v", rec.Code, body)
	}
	if _, body := do(t, h, "GET", path, ""); body["type"] != "payment_account" || body["balance"] != "100.50" || body["updated_at"] != nil {
		t.Errorf("after rejected PUTs: %v", body)
	}
	// PUT with the same type (round-trip of a GET response, any case) works.
	rec, body := do(t, h, "PUT", path, `{"uid":"`+orig["uid"].(string)+`","name":"KBank Main","type":"Payment_Account","currency":"THB","balance":"99"}`)
	if rec.Code != 200 || body["type"] != "payment_account" || body["balance"] != "99.00" || body["description"] != "" ||
		body["uid"] != orig["uid"] || body["created_at"] != orig["created_at"] || body["updated_at"] == nil {
		t.Errorf("PUT same type: %d %v", rec.Code, body)
	}
	// PUT errors.
	if rec, _ := do(t, h, "PUT", "/api/balances/00000000-0000-4000-8000-000000009999", `{"name":"x","type":"payment_account","currency":"THB","balance":"1"}`); rec.Code != 404 {
		t.Errorf("PUT missing: %d", rec.Code)
	}
	if rec, _ := do(t, h, "PUT", "/api/balances/00000000-0000-4000-8000-000000009999", `{"name":""}`); rec.Code != 404 {
		t.Errorf("PUT missing + invalid: %d", rec.Code)
	}
	if rec, _ := do(t, h, "PUT", path, `{"name":"other","type":"payment_account","currency":"THB","balance":"1"}`); rec.Code != 409 {
		t.Errorf("PUT duplicate name: %d", rec.Code)
	}

	// PATCH partial; sending the stored type is allowed.
	rec, body = do(t, h, "PATCH", path, `{"type":"payment_account","balance":"-12"}`)
	if rec.Code != 200 || body["balance"] != "-12.00" || body["name"] != "KBank Main" || body["type"] != "payment_account" {
		t.Errorf("PATCH same type: %d %v", rec.Code, body)
	}
	// PATCH with another type is 422 on "type", with or without amounts.
	for _, b := range []string{`{"type":"credit_card"}`, `{"type":"credit_card","debt":"1","limit":"2"}`, `{"type":"other_asset"}`} {
		rec, body := do(t, h, "PATCH", path, b)
		if rec.Code != 422 || fieldsOf(body)["type"] != "balance type cannot be changed after creation" {
			t.Errorf("PATCH type change %s: %d %v", b, rec.Code, body)
		}
	}
	// The amount edits above recorded balance adjustments, so KBank's
	// currency is now fixed (409 balance_in_use).
	if rec, body := do(t, h, "PATCH", path, `{"currency":"KWD"}`); rec.Code != 409 || code(body) != "balance_in_use" {
		t.Errorf("PATCH currency of referenced balance: %d %v", rec.Code, body)
	}
	// PATCH currency re-scale / rejection on a balance nothing references
	// (a currency change records no adjustment, so it stays unreferenced).
	rpath, _ := createBal(t, h, `{"name":"Rescale","type":"payment_account","currency":"THB","balance":"-12"}`)
	rec, body = do(t, h, "PATCH", rpath, `{"currency":"KWD"}`)
	if rec.Code != 200 || body["balance"] != "-12.000" || body["balance_minor"].(float64) != -12000 {
		t.Errorf("PATCH KWD: %d %v", rec.Code, body)
	}
	if rec, body := do(t, h, "PATCH", rpath, `{"currency":"THB","balance":"-12.5"}`); rec.Code != 200 {
		t.Errorf("PATCH back to THB: %d %v", rec.Code, body)
	}
	rec, body = do(t, h, "PATCH", rpath, `{"currency":"JPY"}`)
	if rec.Code != 422 || fieldsOf(body)["balance"] == nil {
		t.Errorf("PATCH JPY with fractional balance: %d %v", rec.Code, body)
	}
	// PATCH errors.
	for _, c := range []struct {
		path, body string
		status     int
	}{
		{"/api/balances/00000000-0000-4000-8000-000000009999", `{"name":"x"}`, 404},
		{"/api/balances/abc", `{"name":"x"}`, 400}, // malformed uid
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
	if got["balance"] != "-12.00" || got["currency"] != "THB" {
		t.Errorf("failed PATCHes changed the record: %v", got)
	}
}

func looksLikeUID(v any) bool {
	s, _ := v.(string)
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		case 14:
			if c != '4' {
				return false
			}
		default:
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
				return false
			}
		}
	}
	return true
}

func TestBalanceUIDAssignedAndLookup(t *testing.T) {
	h := newTestServer(t)
	path, created := createBal(t, h, `{"name":"Cash","type":"payment_account","currency":"THB","balance":"100","uid":"00000000-0000-4000-8000-000000000099"}`)
	uid, _ := created["uid"].(string)
	if !looksLikeUID(uid) || uid == "00000000-0000-4000-8000-000000000099" {
		t.Fatalf("create ignored client uid incorrectly: %v", created["uid"])
	}
	if _, hasID := created["id"]; hasID {
		t.Errorf("response must not include id: %v", created)
	}
	rec, byUID := do(t, h, "GET", path, "")
	if rec.Code != 200 || byUID["uid"] != uid {
		t.Fatalf("get by uid: %d %v", rec.Code, byUID)
	}
	if rec, _ := do(t, h, "GET", "/api/balances/00000000-0000-4000-8000-000000000001", ""); rec.Code != 404 {
		t.Errorf("missing uid: %d", rec.Code)
	}
	// PUT with uid in body: ignored; uid unchanged.
	rec, body := do(t, h, "PUT", "/api/balances/"+uid,
		`{"uid":"11111111-1111-4111-8111-111111111111","name":"Cash","type":"payment_account","currency":"THB","balance":"200"}`)
	if rec.Code != 200 || body["uid"] != uid || body["balance"] != "200.00" {
		t.Errorf("PUT by uid: %d %v", rec.Code, body)
	}
	// PATCH with only uid: empty after ignore -> 400.
	rec, body = do(t, h, "PATCH", path, `{"uid":"11111111-1111-4111-8111-111111111111"}`)
	if rec.Code != 400 {
		t.Errorf("PATCH uid-only: %d %v", rec.Code, body)
	}
	// PATCH with uid + real field: uid ignored, field applied.
	rec, body = do(t, h, "PATCH", "/api/balances/"+uid, `{"uid":"11111111-1111-4111-8111-111111111111","balance":"-5"}`)
	if rec.Code != 200 || body["uid"] != uid || body["balance"] != "-5.00" {
		t.Errorf("PATCH with uid: %d %v", rec.Code, body)
	}
	// DELETE by uid.
	if rec, _ := do(t, h, "DELETE", "/api/balances/"+uid, ""); rec.Code != http.StatusNoContent {
		t.Errorf("delete by uid: %d", rec.Code)
	}
	if rec, _ := do(t, h, "GET", path, ""); rec.Code != 404 {
		t.Errorf("get after delete: %d", rec.Code)
	}
}
