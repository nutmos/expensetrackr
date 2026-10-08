package api

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestCreateGetDelete(t *testing.T) {
	h := newTestServer(t)
	uid := seedPayable(t, h, "KBank debit", "THB")

	rec, body := do(t, h, "POST", "/api/transactions",
		`{"amount":"120.5","currency":"thb","balance_uid":"`+uid+`","spent_at":"2026-10-06T21:06:00+08:00","note":"lunch"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status %d body %s", rec.Code, rec.Body)
	}
	if body["amount"] != "120.50" || body["currency"] != "THB" || body["amount_minor"].(float64) != 12050 {
		t.Errorf("unexpected create body: %v", body)
	}
	if body["balance_uid"] != uid || body["account"] != "KBank debit" {
		t.Errorf("payment account: %v", body)
	}
	if body["spent_at"] != "2026-10-06T21:06:00+08:00" {
		t.Errorf("spent_at not preserved: %v", body["spent_at"])
	}
	id := body["uid"].(string)
	if loc := rec.Header().Get("Location"); loc == "" {
		t.Errorf("missing Location header")
	}

	rec, body = do(t, h, "GET", "/api/transactions/"+id, "")
	if rec.Code != http.StatusOK || body["account"] != "KBank debit" || body["balance_uid"] != uid || body["note"] != "lunch" {
		t.Fatalf("get: status %d body %v", rec.Code, body)
	}

	rec, _ = do(t, h, "DELETE", "/api/transactions/"+id, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: status %d", rec.Code)
	}
	rec, _ = do(t, h, "GET", "/api/transactions/"+id, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete: status %d", rec.Code)
	}
}

func TestCreateValidationErrors(t *testing.T) {
	h := newTestServer(t)
	uid := seedPayable(t, h, "Cash", "USD")
	asset, _ := do(t, h, "POST", "/api/balances", `{"name":"Gold","type":"other_asset","currency":"THB","balance":"1"}`)
	if asset.Code != 201 {
		t.Fatalf("seed asset: %d", asset.Code)
	}
	assetUID := func() string {
		_, b := do(t, h, "GET", "/api/balances?type=other_asset", "")
		return b["balances"].([]any)[0].(map[string]any)["uid"].(string)
	}()

	cases := []struct {
		name, body string
		status     int
		field      string
	}{
		{"missing offset", `{"amount":"10","currency":"SGD","balance_uid":"` + uid + `","spent_at":"2026-10-06T21:06:00"}`, 422, "spent_at"},
		{"bad currency", `{"amount":"10","currency":"ABC","balance_uid":"` + uid + `","spent_at":"2026-10-06T21:06:00+08:00"}`, 422, "currency"},
		{"negative amount", `{"amount":"-10","currency":"USD","balance_uid":"` + uid + `","spent_at":"2026-10-06T21:06:00+08:00"}`, 422, "amount"},
		{"too many decimals", `{"amount":"1.005","currency":"USD","balance_uid":"` + uid + `","spent_at":"2026-10-06T21:06:00+08:00"}`, 422, "amount"},
		{"missing balance_uid", `{"amount":"10","currency":"USD","spent_at":"2026-10-06T21:06:00+08:00"}`, 422, "balance_uid"},
		{"unknown balance", `{"amount":"10","currency":"USD","balance_uid":"00000000-0000-4000-8000-000000000099","spent_at":"2026-10-06T21:06:00+08:00"}`, 422, "balance_uid"},
		{"non-payable balance", `{"amount":"10","currency":"THB","balance_uid":"` + assetUID + `","spent_at":"2026-10-06T21:06:00+08:00"}`, 422, "balance_uid"},
		{"legacy account field", `{"amount":"10","currency":"USD","account":"Cash","spent_at":"2026-10-06T21:06:00+08:00"}`, 400, ""},
		{"malformed json", `{"amount":`, 400, ""},
		{"unknown field", `{"amount":"10","currency":"USD","balance_uid":"` + uid + `","spent_at":"2026-10-06T21:06:00+08:00","tip":1}`, 400, ""},
		{"empty body", ``, 400, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec, body := do(t, h, "POST", "/api/transactions", c.body)
			if rec.Code != c.status {
				t.Fatalf("status %d, want %d; body %s", rec.Code, c.status, rec.Body)
			}
			if body["error"] == nil {
				t.Errorf("missing error message: %v", body)
			}
			if c.field != "" {
				fields, _ := body["fields"].(map[string]any)
				if fields[c.field] == nil {
					t.Errorf("expected field error for %q, got %v", c.field, body)
				}
			}
		})
	}
}

func TestListOrderingAndFilters(t *testing.T) {
	h := newTestServer(t)
	cash := seedPayable(t, h, "Cash", "THB")
	card := seedCard(t, h, "Card", "USD")
	inputs := []string{
		`{"amount":"1","currency":"THB","balance_uid":"` + cash + `","spent_at":"2026-10-05T09:00:00+07:00"}`,
		`{"amount":"2","currency":"THB","balance_uid":"` + cash + `","spent_at":"2026-10-05T12:00:00+08:00"}`,
		`{"amount":"3","currency":"USD","balance_uid":"` + card + `","spent_at":"2026-10-05T03:00:00Z"}`,
	}
	for _, in := range inputs {
		if rec, _ := do(t, h, "POST", "/api/transactions", in); rec.Code != http.StatusCreated {
			t.Fatalf("seed: %d %s", rec.Code, rec.Body)
		}
	}

	rec, body := do(t, h, "GET", "/api/transactions", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	got := amounts(body)
	if strings.Join(got, ",") != "2.00,3.00,1.00" {
		t.Errorf("order = %v, want newest first [2.00 3.00 1.00]", got)
	}

	q := url.Values{"from": {"2026-10-05T10:30:00+08:00"}, "to": {"2026-10-05T11:30:00+08:00"}}
	rec, body = do(t, h, "GET", "/api/transactions?"+q.Encode(), "")
	if rec.Code != http.StatusOK || strings.Join(amounts(body), ",") != "3.00" {
		t.Errorf("filtered = %d %v, want [3.00]", rec.Code, amounts(body))
	}
}

func amounts(body map[string]any) []string {
	var out []string
	items, _ := body["transactions"].([]any)
	for _, it := range items {
		out = append(out, it.(map[string]any)["amount"].(string))
	}
	return out
}
