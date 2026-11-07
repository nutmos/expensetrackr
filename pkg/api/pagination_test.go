package api

import (
	"fmt"
	"net/url"
	"testing"
	"testing/fstest"

	"github.com/nutmos/expensetrackr/pkg/store"
	"github.com/nutmos/expensetrackr/pkg/store/storetest"

	"github.com/gin-gonic/gin"
)

func expenseAt(bal, amount, at string) string {
	return `{"type":"expense","amount":"` + amount + `","currency":"THB","balance_uid":"` + bal + `","spent_at":"` + at + `"}`
}

func uidsOf(t *testing.T, b map[string]any) []string {
	t.Helper()
	var out []string
	for _, x := range b["transactions"].([]any) {
		out = append(out, x.(map[string]any)["uid"].(string))
	}
	return out
}

// TestTransactionPagination (issue #86): keyset pages cover every row exactly
// once in list order, ties on spent_at included, and stay consistent when
// rows are added or deleted between pages.
func TestTransactionPagination(t *testing.T) {
	h := newTestServer(t)
	cash := seedPayable(t, h, "Cash", "THB")
	// 7 rows; three share one timestamp to exercise the id tie-break.
	times := []string{"2026-10-01T08:00:00Z", "2026-10-02T08:00:00Z", "2026-10-03T08:00:00Z", "2026-10-03T08:00:00Z",
		"2026-10-03T08:00:00Z", "2026-10-04T08:00:00+07:00", "2026-10-05T08:00:00Z"}
	for i, at := range times {
		if rec, b := do(t, h, "POST", "/api/transactions", expenseAt(cash, fmt.Sprint(i+1), at)); rec.Code != 201 {
			t.Fatalf("seed %d: %d %v", i, rec.Code, b)
		}
	}
	_, all := do(t, h, "GET", "/api/transactions", "")
	want := uidsOf(t, all)
	if len(want) != 7 || all["next_cursor"] != nil {
		t.Fatalf("unpaged list: %d rows, next_cursor %v", len(want), all["next_cursor"])
	}

	var got []string
	cursor, pages := "", 0
	for {
		q := "/api/transactions?limit=3"
		if cursor != "" {
			q += "&cursor=" + url.QueryEscape(cursor)
		}
		rec, b := do(t, h, "GET", q, "")
		if rec.Code != 200 {
			t.Fatalf("page %d: %d %v", pages, rec.Code, b)
		}
		pages++
		got = append(got, uidsOf(t, b)...)
		if int(b["count"].(float64)) != len(uidsOf(t, b)) {
			t.Errorf("count %v vs %d rows", b["count"], len(uidsOf(t, b)))
		}
		next, _ := b["next_cursor"].(string)
		if next == "" {
			break
		}
		cursor = next
		if pages > 5 {
			t.Fatal("pagination does not end")
		}
	}
	if pages != 3 || fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("pages %d\n got  %v\n want %v", pages, got, want)
	}

	// An exact multiple of the page size: the last full page has no cursor.
	_, b := do(t, h, "GET", "/api/transactions?limit=7", "")
	if b["next_cursor"] != nil || len(uidsOf(t, b)) != 7 {
		t.Errorf("limit=7: next_cursor %v, %d rows", b["next_cursor"], len(uidsOf(t, b)))
	}

	// Rows added or deleted after page 1 don't shift page 2.
	_, p1 := do(t, h, "GET", "/api/transactions?limit=3", "")
	next := p1["next_cursor"].(string)
	if rec, _ := do(t, h, "DELETE", "/api/transactions/"+want[0], ""); rec.Code != 204 { // newest, already shown
		t.Fatalf("delete: %d", rec.Code)
	}
	if rec, _ := do(t, h, "POST", "/api/transactions", expenseAt(cash, "99", "2026-10-06T08:00:00Z")); rec.Code != 201 { // newer than page 1
		t.Fatalf("insert: %d", rec.Code)
	}
	_, p2 := do(t, h, "GET", "/api/transactions?limit=3&cursor="+url.QueryEscape(next), "")
	if fmt.Sprint(uidsOf(t, p2)) != fmt.Sprint(want[3:6]) {
		t.Errorf("page 2 after changes: %v, want %v", uidsOf(t, p2), want[3:6])
	}

	// Malformed cursors are a 400 on "cursor".
	for _, bad := range []string{"not-a-cursor", "AAAA", store.ListCursor{SpentAtUnix: 1, ID: 0}.String()} {
		rec, b := do(t, h, "GET", "/api/transactions?cursor="+url.QueryEscape(bad), "")
		if rec.Code != 400 || fieldsOf(b)["cursor"] == nil {
			t.Errorf("cursor %q: %d %v", bad, rec.Code, b)
		}
	}
}

// TestTransactionBalanceFilter (issue #86): balance_uid matches the source
// or the destination of a transaction, and only the user's own rows.
func TestTransactionBalanceFilter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	st := storetest.Open(t)
	router := (&Server{Store: st, Static: fstest.MapFS{"index.html": {Data: []byte("x")}}}).Router()
	alice := asUser(t, router, st, "alice")
	bob := asUser(t, router, st, "bob")

	cash := seedPayable(t, alice, "Cash", "THB")
	bank := seedPayable(t, alice, "Bank", "THB")
	card := seedCard(t, alice, "Card", "THB")
	post := func(body string) string {
		t.Helper()
		rec, b := do(t, alice, "POST", "/api/transactions", body)
		if rec.Code != 201 {
			t.Fatalf("create: %d %v", rec.Code, b)
		}
		return b["uid"].(string)
	}
	e1 := post(expenseAt(cash, "10", "2026-10-01T08:00:00Z"))
	post(expenseAt(card, "20", "2026-10-02T08:00:00Z"))
	t1 := post(transferBody(bank, cash, "30", "THB", "2026-10-03T08:00:00Z", "")) // cash is the destination
	t2 := post(transferBody(cash, bank, "40", "THB", "2026-10-04T08:00:00Z", "")) // cash is the source

	rec, b := do(t, alice, "GET", "/api/transactions?balance_uid="+cash, "")
	if rec.Code != 200 || fmt.Sprint(uidsOf(t, b)) != fmt.Sprint([]string{t2, t1, e1}) {
		t.Errorf("cash rows: %d %v, want %v", rec.Code, uidsOf(t, b), []string{t2, t1, e1})
	}
	// Combined with type and paging.
	_, b = do(t, alice, "GET", "/api/transactions?balance_uid="+cash+"&type=transfer&limit=1", "")
	if fmt.Sprint(uidsOf(t, b)) != fmt.Sprint([]string{t2}) || b["next_cursor"] == nil {
		t.Errorf("cash transfers page 1: %v next %v", uidsOf(t, b), b["next_cursor"])
	}
	_, b = do(t, alice, "GET", "/api/transactions?balance_uid="+cash+"&type=transfer&limit=1&cursor="+url.QueryEscape(b["next_cursor"].(string)), "")
	if fmt.Sprint(uidsOf(t, b)) != fmt.Sprint([]string{t1}) || b["next_cursor"] != nil {
		t.Errorf("cash transfers page 2: %v next %v", uidsOf(t, b), b["next_cursor"])
	}
	// Another user's balance matches nothing; a bad uid is a 400.
	if rec, b := do(t, bob, "GET", "/api/transactions?balance_uid="+cash, ""); rec.Code != 200 || len(uidsOf(t, b)) != 0 {
		t.Errorf("bob with alice's balance: %d %v", rec.Code, b)
	}
	if rec, b := do(t, alice, "GET", "/api/transactions?balance_uid=nope", ""); rec.Code != 400 || fieldsOf(b)["balance_uid"] == nil {
		t.Errorf("bad balance_uid: %d %v", rec.Code, b)
	}
}
