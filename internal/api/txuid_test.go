package api

import (
	"fmt"
	"net/http"
	"regexp"
	"testing"
)

var uuidV4RE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestTransactionUID(t *testing.T) {
	h := newTestServer(t)
	bal := seedPayable(t, h, "KBank debit", "THB")
	body := func(amount, extra string) string {
		return fmt.Sprintf(`{"amount":%q,"currency":"THB","balance_uid":%q,"spent_at":"2026-10-06T21:06:00+08:00"%s}`, amount, bal, extra)
	}
	rec, created := do(t, h, http.MethodPost, "/api/expenses",
		body("120.50", `,"uid":"00000000-0000-4000-8000-000000000000"`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %v", rec.Code, created)
	}
	uid, _ := created["uid"].(string)
	if !uuidV4RE.MatchString(uid) || uid == "00000000-0000-4000-8000-000000000000" {
		t.Fatalf("create uid = %q (must be server-assigned UUID v4)", uid)
	}
	id := int64(created["id"].(float64))

	// GET by uid and by id; list includes uid.
	for _, p := range []string{fmt.Sprintf("/api/expenses/%d", id), "/api/expenses/" + uid} {
		rec, got := do(t, h, http.MethodGet, p, "")
		if rec.Code != 200 || got["uid"] != uid {
			t.Errorf("GET %s: %d %v", p, rec.Code, got)
		}
	}
	_, list := do(t, h, http.MethodGet, "/api/expenses", "")
	if items := list["expenses"].([]any); items[0].(map[string]any)["uid"] != uid {
		t.Errorf("list missing uid: %v", items)
	}

	// PUT (by uid) with a different uid in body: ignored.
	rec, put := do(t, h, http.MethodPut, "/api/expenses/"+uid,
		body("150", `,"uid":"11111111-1111-4111-8111-111111111111"`))
	if rec.Code != 200 || put["uid"] != uid || put["amount"] != "150.00" {
		t.Errorf("PUT: %d %v", rec.Code, put)
	}
	// PATCH with uid in body: ignored, uid unchanged.
	rec, pat := do(t, h, http.MethodPatch, fmt.Sprintf("/api/expenses/%d", id),
		`{"note":"x","uid":"22222222-2222-4222-8222-222222222222"}`)
	if rec.Code != 200 || pat["uid"] != uid || pat["note"] != "x" {
		t.Errorf("PATCH: %d %v", rec.Code, pat)
	}

	// Unknown uid -> 404; DELETE by uid -> 204.
	if rec, _ := do(t, h, http.MethodGet, "/api/expenses/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", ""); rec.Code != 404 {
		t.Errorf("unknown uid: %d", rec.Code)
	}
	if rec, _ := do(t, h, http.MethodDelete, "/api/expenses/"+uid, ""); rec.Code != 204 {
		t.Errorf("delete by uid: %d", rec.Code)
	}
	if rec, _ := do(t, h, http.MethodGet, fmt.Sprintf("/api/expenses/%d", id), ""); rec.Code != 404 {
		t.Errorf("after delete: %d", rec.Code)
	}
}
