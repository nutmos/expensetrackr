package api

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

var uuidV4RE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestTransactionUID(t *testing.T) {
	h := newTestServer(t)
	bal := seedPayable(t, h, "KBank debit", "THB")
	body := func(amount, extra string) string {
		return fmt.Sprintf(`{"amount":%q,"currency":"THB","balance_uid":%q,"spent_at":"2026-10-06T21:06:00+08:00"%s}`, amount, bal, extra)
	}
	rec, created := do(t, h, http.MethodPost, "/api/transactions",
		body("120.50", `,"uid":"00000000-0000-4000-8000-000000000000"`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %v", rec.Code, created)
	}
	uid, _ := created["uid"].(string)
	if !uuidV4RE.MatchString(uid) || uid == "00000000-0000-4000-8000-000000000000" {
		t.Fatalf("create uid = %q (must be server-assigned UUID v4)", uid)
	}
	if _, hasID := created["id"]; hasID {
		t.Errorf("response must not include id: %v", created)
	}
	if loc := rec.Header().Get("Location"); loc != "/api/transactions/"+uid {
		t.Errorf("Location = %q", loc)
	}

	// GET by uid (upper-case accepted); list includes uid; numeric id is 400.
	if rec, _ := do(t, h, http.MethodGet, "/api/transactions/1", ""); rec.Code != 400 {
		t.Errorf("numeric id: %d, want 400", rec.Code)
	}
	for _, p := range []string{"/api/transactions/" + uid, "/api/transactions/" + strings.ToUpper(uid)} {
		rec, got := do(t, h, http.MethodGet, p, "")
		if rec.Code != 200 || got["uid"] != uid {
			t.Errorf("GET %s: %d %v", p, rec.Code, got)
		}
	}
	_, list := do(t, h, http.MethodGet, "/api/transactions", "")
	if items := list["transactions"].([]any); items[0].(map[string]any)["uid"] != uid {
		t.Errorf("list missing uid: %v", items)
	}

	// PUT (by uid) with a different uid in body: ignored.
	rec, put := do(t, h, http.MethodPut, "/api/transactions/"+uid,
		body("150", `,"uid":"11111111-1111-4111-8111-111111111111"`))
	if rec.Code != 200 || put["uid"] != uid || put["amount"] != "150.00" {
		t.Errorf("PUT: %d %v", rec.Code, put)
	}
	// PUT with uid in body: ignored, uid unchanged.
	rec, pat := putMerged(t, h, "/api/transactions/"+uid,
		`{"note":"x","uid":"22222222-2222-4222-8222-222222222222"}`)
	if rec.Code != 200 || pat["uid"] != uid || pat["note"] != "x" {
		t.Errorf("PUT: %d %v", rec.Code, pat)
	}

	// Unknown uid -> 404; DELETE by uid -> 204.
	if rec, _ := do(t, h, http.MethodGet, "/api/transactions/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", ""); rec.Code != 404 {
		t.Errorf("unknown uid: %d", rec.Code)
	}
	if rec, _ := do(t, h, http.MethodDelete, "/api/transactions/"+uid, ""); rec.Code != 204 {
		t.Errorf("delete by uid: %d", rec.Code)
	}
	if rec, _ := do(t, h, http.MethodGet, "/api/transactions/"+uid, ""); rec.Code != 404 {
		t.Errorf("after delete: %d", rec.Code)
	}
}

func TestOldExpensesPathsRemoved(t *testing.T) {
	h := newTestServer(t)
	for _, m := range []string{http.MethodGet, http.MethodPost} {
		if rec, _ := do(t, h, m, "/api/expenses", ""); rec.Code != http.StatusNotFound {
			t.Errorf("%s /api/expenses: %d, want 404", m, rec.Code)
		}
	}
	if rec, _ := do(t, h, http.MethodGet, "/api/expenses/1", ""); rec.Code != http.StatusNotFound {
		t.Errorf("GET /api/expenses/1: %d, want 404", rec.Code)
	}
}
