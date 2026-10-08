package api

import (
	"net/http"
	"strings"
	"sync"
	"testing"
)

func ifMatch(v string) map[string]string { return map[string]string{"If-Match": v} }

func code(body map[string]any) string { s, _ := body["code"].(string); return s }

func TestBalanceVersioningAPI(t *testing.T) {
	h := newTestServer(t)
	rec, b := doH(t, h, "POST", "/api/balances", `{"name":"Cash","type":"payment_account","currency":"THB","balance":"100"}`, nil)
	if rec.Code != 201 || b["version"].(float64) != 1 || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("create: %d %v etag %q", rec.Code, b, rec.Header().Get("ETag"))
	}
	path := "/api/balances/" + b["uid"].(string)
	if rec, _ := doH(t, h, "GET", path, "", nil); rec.Header().Get("ETag") != `"1"` {
		t.Errorf("GET etag %q", rec.Header().Get("ETag"))
	}
	put := `{"name":"Cash","type":"payment_account","currency":"THB","balance":"90"}`

	// Missing version: 428, nothing changes.
	for _, m := range []string{"PUT", "PATCH"} {
		rec, body := doH(t, h, m, path, put, nil)
		if rec.Code != http.StatusPreconditionRequired || code(body) != "version_required" {
			t.Errorf("%s without version: %d %v", m, rec.Code, body)
		}
	}
	// Malformed / disagreeing preconditions: 400.
	for _, c := range []struct {
		hdr  map[string]string
		body string
	}{
		{ifMatch("1"), put},        // unquoted
		{ifMatch(`"abc"`), put},    // not a number
		{ifMatch(`"1", "2"`), put}, // list
		{ifMatch("*"), put},        // wildcard not supported
		{ifMatch(`"0"`), put},      // not positive
		{ifMatch(`"1"`), strings.Replace(put, `{`, `{"version":2,`, 1)}, // disagree
		{nil, strings.Replace(put, `{`, `{"version":0,`, 1)},
		{nil, strings.Replace(put, `{`, `{"version":"1",`, 1)},
	} {
		if rec, body := doH(t, h, "PUT", path, c.body, c.hdr); rec.Code != 400 {
			t.Errorf("PUT %v %s: %d %v", c.hdr, c.body, rec.Code, body)
		}
	}
	if rec, body := doH(t, h, "PATCH", path, `{"version":"x","balance":"1"}`, nil); rec.Code != 400 {
		t.Errorf("PATCH bad version: %d %v", rec.Code, body)
	}
	if _, body := doH(t, h, "GET", path, "", nil); body["balance"] != "100.00" || body["version"].(float64) != 1 {
		t.Fatalf("rejected writes changed the balance: %v", body)
	}

	// If-Match with the current version: 200, new ETag.
	rec, body := doH(t, h, "PUT", path, put, ifMatch(`"1"`))
	if rec.Code != 200 || body["version"].(float64) != 2 || rec.Header().Get("ETag") != `"2"` || body["balance"] != "90.00" {
		t.Fatalf("PUT v1: %d %v", rec.Code, body)
	}
	// Same stale If-Match again: 409 with the current state, nothing saved.
	rec, body = doH(t, h, "PUT", path, strings.Replace(put, "90", "80", 1), ifMatch(`"1"`))
	cur, _ := body["current"].(map[string]any)
	if rec.Code != 409 || code(body) != "version_conflict" || cur["version"].(float64) != 2 || cur["balance"] != "90.00" || rec.Header().Get("ETag") != `"2"` {
		t.Errorf("stale PUT: %d %v", rec.Code, body)
	}
	// Stale beats invalid: a stale client must reload before anything else.
	if rec, body := doH(t, h, "PATCH", path, `{"balance":"x"}`, ifMatch(`"1"`)); rec.Code != 409 {
		t.Errorf("stale + invalid PATCH: %d %v", rec.Code, body)
	}
	// Version in the body (PATCH), and a weak ETag (PUT), are accepted.
	if rec, body := doH(t, h, "PATCH", path, `{"version":2,"balance":"70"}`, nil); rec.Code != 200 || body["version"].(float64) != 3 {
		t.Errorf("PATCH body version: %d %v", rec.Code, body)
	}
	if rec, body := doH(t, h, "PUT", path, put, ifMatch(`W/"3"`)); rec.Code != 200 || body["version"].(float64) != 4 {
		t.Errorf("PUT weak etag: %d %v", rec.Code, body)
	}
	// Body version equal to If-Match is fine (a GET response sent back).
	if rec, body := doH(t, h, "PUT", path, strings.Replace(put, `{`, `{"version":4,`, 1), ifMatch(`"4"`)); rec.Code != 200 {
		t.Errorf("PUT both agree: %d %v", rec.Code, body)
	}
	// DELETE: If-Match optional; stale -> 409.
	if rec, body := doH(t, h, "DELETE", path, "", ifMatch(`"2"`)); rec.Code != 409 || code(body) != "version_conflict" {
		t.Errorf("stale DELETE: %d %v", rec.Code, body)
	}
	if rec, _ := doH(t, h, "DELETE", path, "", ifMatch(`"5"`)); rec.Code != 204 {
		t.Errorf("DELETE current: %d", rec.Code)
	}
	// Unknown / malformed uid still 404 / 400 before any precondition.
	if rec, _ := doH(t, h, "PUT", path, put, nil); rec.Code != 404 {
		t.Errorf("PUT deleted: %d", rec.Code)
	}
	if rec, _ := doH(t, h, "PATCH", "/api/balances/abc", `{}`, nil); rec.Code != 400 {
		t.Errorf("PATCH bad uid: %d", rec.Code)
	}
}

func TestTransactionVersioningAPI(t *testing.T) {
	h := newTestServer(t)
	cash := seedPayable(t, h, "Cash", "THB") // balance 10000.00
	body := `{"amount":"100","currency":"THB","balance_uid":"` + cash + `","spent_at":"2026-10-08T12:00:00+08:00"}`
	rec, tx := doH(t, h, "POST", "/api/transactions", body, nil)
	if rec.Code != 201 || tx["version"].(float64) != 1 || tx["adjusts_balances"] != true || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("create: %d %v", rec.Code, tx)
	}
	path := "/api/transactions/" + tx["uid"].(string)
	if rec, _ := doH(t, h, "GET", path, "", nil); rec.Header().Get("ETag") != `"1"` {
		t.Errorf("GET etag %q", rec.Header().Get("ETag"))
	}
	for _, m := range []string{"PUT", "PATCH"} {
		rec, b := doH(t, h, m, path, `{"amount":"5","currency":"THB","balance_uid":"`+cash+`","spent_at":"2026-10-08T12:00:00+08:00"}`, nil)
		if rec.Code != 428 || code(b) != "version_required" {
			t.Errorf("%s without version: %d %v", m, rec.Code, b)
		}
	}
	// PUT with body version, then the same version again is stale.
	put := `{"version":1,"amount":"150","currency":"THB","balance_uid":"` + cash + `","spent_at":"2026-10-08T12:00:00+08:00"}`
	if rec, b := doH(t, h, "PUT", path, put, nil); rec.Code != 200 || b["version"].(float64) != 2 || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("PUT: %d %v", rec.Code, b)
	}
	rec, b := doH(t, h, "PUT", path, put, nil)
	if cur, _ := b["current"].(map[string]any); rec.Code != 409 || code(b) != "version_conflict" || cur["amount"] != "150.00" {
		t.Errorf("stale PUT: %d %v", rec.Code, b)
	}
	if rec, b := doH(t, h, "PATCH", path, `{"note":"x"}`, ifMatch(`"1"`)); rec.Code != 409 {
		t.Errorf("stale PATCH: %d %v", rec.Code, b)
	}
	if rec, b := doH(t, h, "PATCH", path, `{"note":"x"}`, ifMatch(`"2"`)); rec.Code != 200 || b["version"].(float64) != 3 {
		t.Errorf("PATCH: %d %v", rec.Code, b)
	}
	if _, bal := doH(t, h, "GET", "/api/balances/"+cash, "", nil); bal["balance"] != "9850.00" {
		t.Errorf("balance after edits: %v", bal["balance"])
	}
	if rec, _ := doH(t, h, "DELETE", path, "", ifMatch(`"2"`)); rec.Code != 409 {
		t.Errorf("stale DELETE: %d", rec.Code)
	}
	if rec, _ := doH(t, h, "DELETE", path, "", nil); rec.Code != 204 {
		t.Errorf("DELETE: %d", rec.Code)
	}
	if _, bal := doH(t, h, "GET", "/api/balances/"+cash, "", nil); bal["balance"] != "10000.00" {
		t.Errorf("balance after delete: %v", bal["balance"])
	}
}

func TestTransactionsMoveBalancesAPI(t *testing.T) {
	h := newTestServer(t)
	cash := seedPayable(t, h, "Cash", "THB") // 10000.00
	card := seedCard(t, h, "Visa", "THB")    // debt 0, limit 50000
	usd := seedPayable(t, h, "Dollars", "USD")
	get := func(uid string) map[string]any { _, b := doH(t, h, "GET", "/api/balances/"+uid, "", nil); return b }
	post := func(body string) (*int, map[string]any) {
		rec, b := doH(t, h, "POST", "/api/transactions", body, nil)
		return &rec.Code, b
	}
	at := `,"spent_at":"2026-10-08T12:00:00+08:00"}`

	// A user opens the Cash edit page (version 1)...
	if v := get(cash)["version"].(float64); v != 1 {
		t.Fatalf("cash version %v", v)
	}
	// ...meanwhile an expense and a card payment are recorded.
	if c, b := post(`{"type":"expense","amount":"250","currency":"THB","balance_uid":"` + card + `"` + at); *c != 201 {
		t.Fatalf("card expense: %d %v", *c, b)
	}
	if c, b := post(`{"type":"transfer","amount":"300","currency":"THB","balance_uid":"` + cash + `","to_balance_uid":"` + card + `"` + at); *c != 201 {
		t.Fatalf("card payment: %d %v", *c, b)
	}
	if b := get(cash); b["balance"] != "9700.00" || b["version"].(float64) != 2 {
		t.Errorf("cash: %v", b)
	}
	if b := get(card); b["debt"] != "-50.00" || b["over_limit"] != false || b["available"] != "50050.00" {
		t.Errorf("card (overpaid): %v", b)
	}
	// ...so saving the stale edit page is a conflict showing the new amount.
	rec, b := doH(t, h, "PUT", "/api/balances/"+cash, `{"name":"Cash","type":"payment_account","currency":"THB","balance":"9999"}`, ifMatch(`"1"`))
	if cur, _ := b["current"].(map[string]any); rec.Code != 409 || cur["balance"] != "9700.00" {
		t.Errorf("stale manual edit: %d %v", rec.Code, b)
	}
	// After reloading, the manual value overrides what transactions did.
	rec, b = doH(t, h, "PUT", "/api/balances/"+cash, `{"name":"Cash","type":"payment_account","currency":"THB","balance":"9690.5"}`, ifMatch(`"2"`))
	if rec.Code != 200 || b["balance"] != "9690.50" || b["version"].(float64) != 3 {
		t.Errorf("manual edit: %d %v", rec.Code, b)
	}
	if rec, b := doH(t, h, "PATCH", "/api/balances/"+cash, `{"balance":"9690.25"}`, ifMatch(`"3"`)); rec.Code != 200 || b["balance"] != "9690.25" || b["version"].(float64) != 4 {
		t.Errorf("manual PATCH: %d %v", rec.Code, b)
	}
	// There is no separate adjustments API or note field: unknown fields are
	// rejected as for any other body, and nothing changes.
	if rec, b := doH(t, h, "PUT", "/api/balances/"+cash, `{"name":"Cash","type":"payment_account","currency":"THB","balance":"1","adjustment_note":"x"}`, ifMatch(`"4"`)); rec.Code != 400 {
		t.Errorf("PUT with adjustment_note: %d %v", rec.Code, b)
	}
	if rec, b := doH(t, h, "PATCH", "/api/balances/"+cash, `{"balance":"1","adjustment_note":"x"}`, ifMatch(`"4"`)); rec.Code < 400 || rec.Code > 422 {
		t.Errorf("PATCH with adjustment_note: %d %v", rec.Code, b)
	}
	if rec, _ := doH(t, h, "GET", "/api/balances/"+cash+"/adjustments", "", nil); rec.Code != 404 {
		t.Errorf("adjustments route still exists: %d", rec.Code)
	}
	if b := get(cash); b["balance"] != "9690.25" || b["version"].(float64) != 4 {
		t.Errorf("rejected edits changed the balance: %v", b)
	}
	// Later transactions continue from the manual value.
	if c, b := post(`{"amount":"0.25","currency":"THB","balance_uid":"` + cash + `"` + at); *c != 201 {
		t.Fatalf("expense after manual edit: %d %v", *c, b)
	}
	if b := get(cash); b["balance"] != "9690.00" || b["version"].(float64) != 5 {
		t.Errorf("after manual edit + expense: %v", b)
	}

	// Currency mismatch: 422, nothing written.
	before := get(cash)
	if c, b := post(`{"amount":"5","currency":"USD","balance_uid":"` + cash + `"` + at); *c != 422 || fieldsOf(b)["balance_uid"] == nil {
		t.Errorf("USD on THB: %d %v", *c, b)
	}
	if c, b := post(`{"type":"transfer","amount":"5","currency":"THB","balance_uid":"` + cash + `","to_balance_uid":"` + usd + `"` + at); *c != 422 || fieldsOf(b)["to_balance_uid"] == nil {
		t.Errorf("THB -> USD transfer: %d %v", *c, b)
	}
	if after := get(cash); after["balance"] != before["balance"] || after["version"] != before["version"] {
		t.Errorf("rejected transactions moved the balance: %v -> %v", before, after)
	}
	// A balance with transactions cannot change currency.
	rec, b = doH(t, h, "PATCH", "/api/balances/"+cash, `{"currency":"USD"}`, ifMatch(`"5"`))
	if rec.Code != 409 || code(b) != "balance_in_use" || fieldsOf(b)["currency"] == nil {
		t.Errorf("currency change in use: %d %v", rec.Code, b)
	}
}

func TestConcurrentBalancePatchesOneWins(t *testing.T) {
	h := newTestServer(t)
	cash := seedPayable(t, h, "Cash", "THB")
	const n = 20
	var wg sync.WaitGroup
	codes := make(chan int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec, _ := doH(t, h, "PATCH", "/api/balances/"+cash, `{"balance":"1"}`, ifMatch(`"1"`))
			codes <- rec.Code
		}()
	}
	wg.Wait()
	close(codes)
	got := map[int]int{}
	for c := range codes {
		got[c]++
	}
	if got[200] != 1 || got[409] != n-1 {
		t.Errorf("status counts = %v, want one 200 and %d 409", got, n-1)
	}
}
