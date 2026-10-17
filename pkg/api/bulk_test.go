package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/nutmos/expensetrackr/pkg/fx"
	"github.com/nutmos/expensetrackr/pkg/store"

	"github.com/gin-gonic/gin"
)

func bulkExpense(bal, amount, ref string) string {
	s := `{"type":"expense","amount":"` + amount + `","currency":"THB","balance_uid":"` + bal + `","spent_at":"2026-10-03T09:00:00+07:00","note":"sample row"`
	if ref != "" {
		s += `,"import_ref":"` + ref + `"`
	}
	return s + `}`
}

func bulkBody(opts string, items ...string) string {
	if opts != "" {
		opts = "," + opts
	}
	return `{"transactions":[` + strings.Join(items, ",") + `]` + opts + `}`
}

func results(t *testing.T, b map[string]any) []map[string]any {
	t.Helper()
	raw, _ := b["results"].([]any)
	out := make([]map[string]any, len(raw))
	for i, r := range raw {
		out[i] = r.(map[string]any)
	}
	return out
}

func statuses(t *testing.T, b map[string]any) string {
	var s []string
	for _, r := range results(t, b) {
		s = append(s, r["status"].(string))
	}
	return strings.Join(s, ",")
}

func txCount(t *testing.T, h http.Handler) int {
	t.Helper()
	_, b := do(t, h, "GET", "/api/transactions", "")
	return int(b["count"].(float64))
}

func cardDebt(t *testing.T, h http.Handler, uid string) string {
	t.Helper()
	_, b := do(t, h, "GET", "/api/balances/"+uid, "")
	return b["debt"].(string)
}

func TestBulkAtomicSuccess(t *testing.T) {
	h := newTestServer(t)
	pay := seedPayable(t, h, "Bank", "THB") // 10000
	card := seedCard(t, h, "Card", "THB")
	food := mkCat(t, h, "Food", "expense")["uid"].(string)
	_, before := do(t, h, "GET", "/api/balances/"+pay, "")
	body := bulkBody("",
		`{"type":"expense","amount":"100.50","currency":"thb","balance_uid":"`+pay+`","category_uid":"`+food+`","spent_at":"2026-10-01T08:00:00+07:00","note":"coffee","import_ref":"r1"}`,
		`{"type":"income","amount":"1000","currency":"THB","balance_uid":"`+pay+`","spent_at":"2026-10-02T08:00:00+07:00"}`,
		`{"type":"expense","amount":"250","currency":"THB","balance_uid":"`+card+`","spent_at":"2026-10-02T09:00:00+07:00","import_ref":"r2"}`,
		`{"type":"transfer","amount":"200","currency":"THB","balance_uid":"`+pay+`","to_balance_uid":"`+card+`","spent_at":"2026-10-03T08:00:00+07:00"}`,
	)
	rec, b := do(t, h, "POST", "/api/transactions/bulk", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("bulk: %d %s", rec.Code, rec.Body)
	}
	if b["created"].(float64) != 4 || b["failed"].(float64) != 0 || b["written"] != true || statuses(t, b) != "created,created,created,created" {
		t.Fatalf("body %v", b)
	}
	rs := results(t, b)
	for i, r := range rs {
		if int(r["index"].(float64)) != i || !looksLikeUID(r["transaction"].(map[string]any)["uid"]) {
			t.Errorf("result %d: %v", i, r)
		}
	}
	if rs[0]["transaction"].(map[string]any)["import_ref"] != "r1" || rs[1]["transaction"].(map[string]any)["import_ref"] != nil {
		t.Errorf("import_ref in JSON: %v", rs)
	}
	// 10000 - 100.50 + 1000 - 200 = 10699.50; card 250 - 200 = 50.
	if got := balanceOf(t, h, pay); got != "10699.50" {
		t.Errorf("bank balance %s", got)
	}
	if got := cardDebt(t, h, card); got != "50.00" {
		t.Errorf("card debt %s", got)
	}
	_, after := do(t, h, "GET", "/api/balances/"+pay, "")
	if after["version"].(float64) != before["version"].(float64)+3 {
		t.Errorf("bank version %v -> %v, want +3", before["version"], after["version"])
	}
	if len(b["balances"].([]any)) != 2 {
		t.Errorf("balances %v", b["balances"])
	}
	// Created rows are readable and ordered by spent_at as usual.
	if n := txCount(t, h); n != 4 {
		t.Errorf("count %d", n)
	}
	uid := rs[0]["transaction"].(map[string]any)["uid"].(string)
	if _, g := do(t, h, "GET", "/api/transactions/"+uid, ""); g["import_ref"] != "r1" || g["note"] != "coffee" {
		t.Errorf("stored %v", g)
	}
}

func TestBulkAtomicFailureWritesNothing(t *testing.T) {
	h := newTestServer(t)
	pay := seedPayable(t, h, "Bank", "THB")
	usd := seedPayable(t, h, "Dollars", "USD")
	body := bulkBody(`"atomic":true`,
		bulkExpense(pay, "10", "a"),
		`{"type":"balance_adjustment","amount":"5","currency":"THB","balance_uid":"`+pay+`","spent_at":"2026-10-03T09:00:00+07:00"}`,
		bulkExpense(usd, "20", ""), // THB on a USD balance
		`{"type":"expense","amount":"-1","currency":"THB","balance_uid":"`+pay+`","spent_at":"2026-10-03"}`,
		`{"type":"expense","amount":"1","currency":"THB","balance_uid":"`+pay+`","spent_at":"2026-10-03T09:00:00Z","bogus":1}`,
		`null`,
		bulkExpense(pay, "30", ""),
	)
	rec, b := do(t, h, "POST", "/api/transactions/bulk", body)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bulk: %d %s", rec.Code, rec.Body)
	}
	if got := statuses(t, b); got != "valid,failed,failed,failed,failed,failed,valid" {
		t.Errorf("statuses %s", got)
	}
	if b["written"] != false || b["created"].(float64) != 0 || b["failed"].(float64) != 5 || b["valid"].(float64) != 2 {
		t.Errorf("counts %v", b)
	}
	rs := results(t, b)
	codes := []string{}
	for _, r := range rs[1:6] {
		codes = append(codes, r["error"].(map[string]any)["code"].(string))
	}
	if strings.Join(codes, ",") != "validation_failed,validation_failed,validation_failed,invalid_item,invalid_item" {
		t.Errorf("codes %v", codes)
	}
	if f := rs[1]["error"].(map[string]any)["fields"].(map[string]any); f["type"] == nil {
		t.Errorf("balance_adjustment fields %v", f)
	}
	if f := rs[2]["error"].(map[string]any)["fields"].(map[string]any); !strings.Contains(fmt.Sprint(f["balance_uid"]), "currencies must match") {
		t.Errorf("currency mismatch fields %v", f)
	}
	if f := rs[3]["error"].(map[string]any)["fields"].(map[string]any); f["amount"] == nil || f["spent_at"] == nil {
		t.Errorf("field errors %v", f)
	}
	if tx := rs[0]["transaction"].(map[string]any); tx["uid"] != "" || tx["amount"] != "10.00" {
		t.Errorf("valid item %v", tx)
	}
	if n := txCount(t, h); n != 0 {
		t.Errorf("wrote %d transactions", n)
	}
	if got := balanceOf(t, h, pay); got != "10000.00" {
		t.Errorf("balance moved to %s", got)
	}
}

func TestBulkPartialMode(t *testing.T) {
	h := newTestServer(t)
	pay := seedPayable(t, h, "Bank", "THB")
	usd := seedPayable(t, h, "Dollars", "USD")
	rec, b := do(t, h, "POST", "/api/transactions/bulk", bulkBody(`"atomic":false`,
		bulkExpense(pay, "10", "p1"), bulkExpense(usd, "20", ""), bulkExpense(pay, "30", "p2"), bulkExpense(pay, "abc", "")))
	if rec.Code != http.StatusOK {
		t.Fatalf("bulk: %d %s", rec.Code, rec.Body)
	}
	if got := statuses(t, b); got != "created,failed,created,failed" {
		t.Errorf("statuses %s", got)
	}
	if b["created"].(float64) != 2 || b["failed"].(float64) != 2 || b["written"] != true {
		t.Errorf("counts %v", b)
	}
	if got := balanceOf(t, h, pay); got != "9960.00" {
		t.Errorf("balance %s", got)
	}
	if got := balanceOf(t, h, usd); got != "10000.00" {
		t.Errorf("usd balance %s", got)
	}
}

func TestBulkDryRun(t *testing.T) {
	h := newTestServer(t)
	pay := seedPayable(t, h, "Bank", "THB")
	card := seedCard(t, h, "Card", "THB")
	_, prev := create(t, h, bulkExpense(pay, "5", "existing"))
	rec, b := do(t, h, "POST", "/api/transactions/bulk", bulkBody(`"dry_run":true`,
		bulkExpense(pay, "10", "d1"), bulkExpense(pay, "5", "existing"), bulkExpense(card, "40", "")))
	if rec.Code != http.StatusOK {
		t.Fatalf("dry run: %d %s", rec.Code, rec.Body)
	}
	if got := statuses(t, b); got != "valid,skipped,valid" || b["dry_run"] != true || b["written"] != false || b["valid"].(float64) != 2 {
		t.Errorf("body %v", b)
	}
	if results(t, b)[1]["existing_uid"] != prev["uid"] {
		t.Errorf("existing uid %v", results(t, b)[1])
	}
	// The would-be balances are reported, but nothing changed.
	got := map[string]string{}
	for _, x := range b["balances"].([]any) {
		m := x.(map[string]any)
		if v, ok := m["balance"].(string); ok {
			got[m["name"].(string)] = v
		} else {
			got[m["name"].(string)] = m["debt"].(string)
		}
	}
	if got["Bank"] != "9985.00" || got["Card"] != "40.00" {
		t.Errorf("projected balances %v", got)
	}
	if n := txCount(t, h); n != 1 {
		t.Errorf("count %d", n)
	}
	if balanceOf(t, h, pay) != "9995.00" || cardDebt(t, h, card) != "0.00" {
		t.Error("dry run moved balances")
	}
}

func TestBulkImportRefIdempotency(t *testing.T) {
	h := newTestServer(t)
	a := seedPayable(t, h, "A", "THB")
	bb := seedPayable(t, h, "B", "THB")
	body := bulkBody("", bulkExpense(a, "10", "ref-1"), bulkExpense(a, "11", "ref-1"), bulkExpense(bb, "12", "ref-1"), bulkExpense(a, "13", ""))
	rec, b := do(t, h, "POST", "/api/transactions/bulk", body)
	if rec.Code != http.StatusCreated || statuses(t, b) != "created,skipped,created,created" {
		t.Fatalf("first: %d %s", rec.Code, rec.Body)
	}
	rs := results(t, b)
	first := rs[0]["transaction"].(map[string]any)["uid"]
	if rs[1]["existing_uid"] != first {
		t.Errorf("in-request duplicate existing_uid %v want %v", rs[1]["existing_uid"], first)
	}
	// Re-import: rows with refs are skipped; the ref-less row would be created again.
	rec, b = do(t, h, "POST", "/api/transactions/bulk", bulkBody("", bulkExpense(a, "10", "ref-1"), bulkExpense(bb, "12", "ref-1")))
	if rec.Code != http.StatusOK || statuses(t, b) != "skipped,skipped" || b["skipped"].(float64) != 2 {
		t.Fatalf("re-import: %d %s", rec.Code, rec.Body)
	}
	if balanceOf(t, h, a) != "9977.00" || balanceOf(t, h, bb) != "9988.00" {
		t.Errorf("balances %s %s", balanceOf(t, h, a), balanceOf(t, h, bb))
	}
	// Single POST with a used ref is 409, on another balance it is fine.
	if rec, b := do(t, h, "POST", "/api/transactions", bulkExpense(a, "1", "ref-1")); rec.Code != http.StatusConflict || code(b) != "duplicate_import_ref" {
		t.Errorf("single dup: %d %v", rec.Code, b)
	}
	c := seedPayable(t, h, "C", "THB")
	path, tx := create(t, h, bulkExpense(c, "1", "ref-1"))
	if tx["import_ref"] != "ref-1" {
		t.Errorf("single create %v", tx)
	}
	// PUT keeps the ref when omitted, conflicts when moved onto a balance that has it, changes it, clears it with "".
	if rec, b := putMerged(t, h, path, `{"balance_uid":"`+a+`"}`); rec.Code != http.StatusConflict || code(b) != "duplicate_import_ref" {
		t.Errorf("put dup: %d %v", rec.Code, b)
	}
	if rec, b := putMerged(t, h, path, `{"note":"edited"}`); rec.Code != 200 || b["import_ref"] != "ref-1" {
		t.Errorf("put keep: %d %v", rec.Code, b)
	}
	if rec, b := putMerged(t, h, path, `{"import_ref":"ref-2"}`); rec.Code != 200 || b["import_ref"] != "ref-2" {
		t.Errorf("put change: %d %v", rec.Code, b)
	}
	if rec, b := putMerged(t, h, path, `{"import_ref":""}`); rec.Code != 200 || b["import_ref"] != nil {
		t.Errorf("put clear: %d %v", rec.Code, b)
	}
	// Too long / control characters.
	long := strings.Repeat("x", 201)
	if rec, b := do(t, h, "POST", "/api/transactions", bulkExpense(a, "1", long)); rec.Code != 422 || fieldsOf(b)["import_ref"] == nil {
		t.Errorf("long ref: %d %v", rec.Code, b)
	}
	if rec, _ := do(t, h, "POST", "/api/transactions", bulkExpense(a, "1", strings.Repeat("é", 200))); rec.Code != 201 {
		t.Errorf("200-char ref: %d", rec.Code)
	}
	if rec, _ := do(t, h, "POST", "/api/transactions", bulkExpense(a, "1", `a\tb`)); rec.Code != 422 {
		t.Errorf("control char ref: %d", rec.Code)
	}
}

func TestBulkRequestErrorsAndLimits(t *testing.T) {
	h := newTestServer(t)
	pay := seedPayable(t, h, "Bank", "THB")
	for _, tc := range []struct {
		body string
		want int
	}{
		{`{"transactions":[]}`, 400},
		{`{}`, 400},
		{`{"transactions":[`, 400},
		{`[]`, 400},
		{`{"transactions":[],"extra":1}`, 400},
		{`{"transactions":{}}`, 400},
	} {
		if rec, _ := do(t, h, "POST", "/api/transactions/bulk", tc.body); rec.Code != tc.want {
			t.Errorf("%s: %d want %d", tc.body, rec.Code, tc.want)
		}
	}
	items := make([]string, 501)
	for i := range items {
		items[i] = bulkExpense(pay, "1", "")
	}
	if rec, b := do(t, h, "POST", "/api/transactions/bulk", bulkBody("", items...)); rec.Code != http.StatusRequestEntityTooLarge || code(b) != "too_many_items" {
		t.Errorf("501 items: %d %v", rec.Code, b)
	}
	big := `{"transactions":[` + bulkExpense(pay, "1", "") + `],"pad":"` + strings.Repeat("x", 3<<20) + `"}`
	if rec, b := do(t, h, "POST", "/api/transactions/bulk", big); rec.Code != 400 || !strings.Contains(fmt.Sprint(b["error"]), "too large") {
		t.Errorf("3 MiB body: %d %v", rec.Code, b["error"])
	}
	if n := txCount(t, h); n != 0 {
		t.Errorf("count %d", n)
	}
}

// TestBulk500Performance imports the maximum batch and checks it stays fast.
func TestBulk500Performance(t *testing.T) {
	h := newTestServer(t)
	pay := seedPayable(t, h, "Bank", "THB")
	card := seedCard(t, h, "Card", "THB")
	food := mkCat(t, h, "Food", "expense")["uid"].(string)
	items := make([]string, MaxBulkItems)
	for i := range items {
		bal := pay
		if i%2 == 1 {
			bal = card
		}
		items[i] = fmt.Sprintf(`{"type":"expense","amount":"1.25","currency":"THB","balance_uid":%q,"category_uid":%q,"spent_at":"2026-10-03T09:%02d:%02d+07:00","note":"sample row %d","import_ref":"perf-%d"}`,
			bal, food, i/60, i%60, i, i)
	}
	body := bulkBody("", items...)
	start := time.Now()
	rec, b := do(t, h, "POST", "/api/transactions/bulk", body)
	elapsed := time.Since(start)
	if rec.Code != http.StatusCreated || b["created"].(float64) != MaxBulkItems {
		t.Fatalf("500 items: %d created=%v", rec.Code, b["created"])
	}
	t.Logf("500 items in %v", elapsed)
	if elapsed > 3*time.Second {
		t.Errorf("500 items took %v (want < 3s)", elapsed)
	}
	if balanceOf(t, h, pay) != "9687.50" || cardDebt(t, h, card) != "312.50" {
		t.Errorf("balances %s %s", balanceOf(t, h, pay), cardDebt(t, h, card))
	}
	start = time.Now()
	rec, b = do(t, h, "POST", "/api/transactions/bulk", body)
	if rec.Code != 200 || b["skipped"].(float64) != MaxBulkItems {
		t.Fatalf("re-import: %d skipped=%v", rec.Code, b["skipped"])
	}
	t.Logf("500 skipped in %v", time.Since(start))
}

func TestBulkCrossCurrencyTransfer(t *testing.T) {
	fake, hits := fakeFrankfurter(t)
	h, _ := newFXServer(t, fake.URL)
	usd := seedPayable(t, h, "Dollars", "USD")
	thb := seedPayable(t, h, "Baht", "THB")
	tr := func(at, extra string) string { return transferBody(usd, thb, "10", "USD", at, extra) }
	rec, b := do(t, h, "POST", "/api/transactions/bulk", bulkBody("",
		tr("2026-10-09T10:00:00Z", ""), tr("2026-10-09T11:00:00Z", `,"import_ref":"x"`), tr("2026-10-09T12:00:00Z", `,"to_amount":"340"`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("bulk fx: %d %s", rec.Code, rec.Body)
	}
	rs := results(t, b)
	t0 := rs[0]["transaction"].(map[string]any)
	if t0["to_amount"] != "336.15" || t0["rate_source"] != "frankfurter" || t0["rate_date"] != "2026-10-09" {
		t.Errorf("auto fx %v", t0)
	}
	if t2 := rs[2]["transaction"].(map[string]any); t2["rate_source"] != "manual" || t2["to_amount"] != "340.00" {
		t.Errorf("manual fx %v", t2)
	}
	if hits.Load() != 1 {
		t.Errorf("frankfurter hits %d, want 1 (same pair+date fetched once)", hits.Load())
	}
	if balanceOf(t, h, usd) != "9970.00" || balanceOf(t, h, thb) != "11012.30" {
		t.Errorf("balances %s %s", balanceOf(t, h, usd), balanceOf(t, h, thb))
	}
	// Dry run computes to_amount without writing.
	rec, b = do(t, h, "POST", "/api/transactions/bulk", bulkBody(`"dry_run":true`, tr("2026-10-10T10:00:00Z", "")))
	if rec.Code != 200 || results(t, b)[0]["transaction"].(map[string]any)["to_amount"] != "336.15" {
		t.Errorf("dry run fx: %d %s", rec.Code, rec.Body)
	}
	// Unsupported pair: an item failure (422 in atomic mode).
	vnd := seedPayable(t, h, "Dong", "VND")
	rec, b = do(t, h, "POST", "/api/transactions/bulk", bulkBody("", transferBody(usd, vnd, "10", "USD", "2026-10-09T10:00:00Z", "")))
	if rec.Code != 422 || results(t, b)[0]["error"].(map[string]any)["code"] != "exchange_rate_unsupported" {
		t.Errorf("unsupported: %d %s", rec.Code, rec.Body)
	}
}

func TestBulkFXUpstreamDown(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	down.Close() // connection refused
	gin.SetMode(gin.TestMode)
	st, err := store.Open(filepath.Join(t.TempDir(), "fx.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	client := &fx.Client{BaseURL: down.URL, Cache: st, HTTP: &http.Client{Timeout: time.Second}}
	h := loggedIn(t, (&Server{Store: st, Static: fstest.MapFS{"index.html": {}}, FX: client}).Router(), st)
	usd := seedPayable(t, h, "Dollars", "USD")
	thb := seedPayable(t, h, "Baht", "THB")
	items := []string{bulkExpense(thb, "10", ""), transferBody(usd, thb, "10", "USD", "2026-10-09T10:00:00Z", ""),
		transferBody(usd, thb, "10", "USD", "2026-10-09T10:00:00Z", `,"to_amount":"330"`)}

	rec, b := do(t, h, "POST", "/api/transactions/bulk", bulkBody("", items...))
	if rec.Code != http.StatusServiceUnavailable || statuses(t, b) != "valid,failed,valid" || b["written"] != false {
		t.Fatalf("atomic: %d %s", rec.Code, rec.Body)
	}
	if results(t, b)[1]["error"].(map[string]any)["code"] != "exchange_rate_unavailable" {
		t.Errorf("code %v", results(t, b)[1])
	}
	if n := txCount(t, h); n != 0 {
		t.Errorf("atomic wrote %d", n)
	}
	rec, b = do(t, h, "POST", "/api/transactions/bulk", bulkBody(`"atomic":false`, items...))
	if rec.Code != 200 || statuses(t, b) != "created,failed,created" {
		t.Fatalf("partial: %d %s", rec.Code, rec.Body)
	}
	if balanceOf(t, h, thb) != "10320.00" {
		t.Errorf("thb %s", balanceOf(t, h, thb))
	}
}

func TestBulkAuthAndCSRF(t *testing.T) {
	gin.SetMode(gin.TestMode)
	st, err := store.Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	router := (&Server{Store: st, Static: fstest.MapFS{"index.html": {}}}).Router()
	body := `{"transactions":[{"amount":"1","currency":"THB","balance_uid":"4dc73a4a-a60b-414e-98bc-3c71298a0af5","spent_at":"2026-10-03T09:00:00Z"}]}`
	if rec, _ := doH(t, router, "POST", "/api/transactions/bulk", body, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("no session: %d", rec.Code)
	}
	h := loggedIn(t, router, st)
	for _, tc := range []struct {
		hdr  map[string]string
		want int
	}{
		{map[string]string{"Origin": "https://evil.example"}, 403},
		{map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		{map[string]string{"Content-Type": "text/plain"}, 415},
	} {
		if rec, _ := doH(t, h, "POST", "/api/transactions/bulk", body, tc.hdr); rec.Code != tc.want {
			t.Errorf("%v: %d want %d", tc.hdr, rec.Code, tc.want)
		}
	}
	// Same-origin with a session passes the guard (then fails validation: unknown balance).
	if rec, _ := doH(t, h, "POST", "/api/transactions/bulk", body, map[string]string{"Sec-Fetch-Site": "same-origin"}); rec.Code != 422 {
		t.Errorf("same-origin: %d", rec.Code)
	}
}
