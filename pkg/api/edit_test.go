package api

import (
	"net/http"
	"testing"
)

func create(t *testing.T, h http.Handler, body string) (string, map[string]any) {
	t.Helper()
	rec, m := do(t, h, "POST", "/api/transactions", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("seed: %d %s", rec.Code, rec.Body)
	}
	return "/api/transactions/" + m["uid"].(string), m
}

func TestPutReplacesTransaction(t *testing.T) {
	h := newTestServer(t)
	uid := seedPayable(t, h, "KBank debit", "THB")
	cash := seedPayable(t, h, "Cash", "SGD")
	path, orig := create(t, h, `{"amount":"120.50","currency":"THB","balance_uid":"`+uid+`","spent_at":"2026-10-05T09:00:00+07:00","note":"lunch"}`)
	if orig["updated_at"] != nil {
		t.Errorf("new transaction should have updated_at null, got %v", orig["updated_at"])
	}

	rec, body := do(t, h, "PUT", path, `{"amount":"18.9","currency":"sgd","balance_uid":"`+cash+`","spent_at":"2026-10-06T12:30:00+08:00"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	if body["amount"] != "18.90" || body["currency"] != "SGD" || body["account"] != "Cash" ||
		body["balance_uid"] != cash || body["spent_at"] != "2026-10-06T12:30:00+08:00" || body["note"] != "" {
		t.Errorf("unexpected put body: %v", body)
	}
	if body["uid"] != orig["uid"] || body["created_at"] != orig["created_at"] {
		t.Errorf("id/created_at must not change: %v vs %v", body, orig)
	}
}

func TestPutErrors(t *testing.T) {
	h := newTestServer(t)
	uid := seedPayable(t, h, "Cash", "USD")
	path, _ := create(t, h, `{"amount":"120.50","currency":"USD","balance_uid":"`+uid+`","spent_at":"2026-10-05T09:00:00+07:00","note":"lunch"}`)
	valid := `{"amount":"1","currency":"USD","balance_uid":"` + uid + `","spent_at":"2026-10-06T12:30:00Z"}`
	cases := []struct {
		name, path, body string
		status           int
		field            string
	}{
		{"missing id", "/api/transactions/00000000-0000-4000-8000-000000009999", valid, 404, ""},
		{"missing id + invalid body", "/api/transactions/00000000-0000-4000-8000-000000009999", `{"amount":"x"}`, 404, ""},
		{"bad id", "/api/transactions/1", valid, 400, ""},
		{"no offset", path, `{"amount":"1","currency":"USD","balance_uid":"` + uid + `","spent_at":"2026-10-06T12:30:00"}`, 422, "spent_at"},
		{"missing fields", path, `{"amount":"1"}`, 422, "currency"},
		{"unknown field", path, `{"amount":"1","currency":"USD","balance_uid":"` + uid + `","spent_at":"2026-10-06T12:30:00Z","id":5}`, 400, ""},
		{"malformed", path, `{"amount":`, 400, ""},
		{"array body", path, `[]`, 400, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec, body := do(t, h, "PUT", c.path, c.body)
			if rec.Code != c.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, c.status, rec.Body)
			}
			if c.field != "" {
				if f, _ := body["fields"].(map[string]any); f[c.field] == nil {
					t.Errorf("want field error %q, got %v", c.field, body)
				}
			}
		})
	}
	_, got := do(t, h, "GET", path, "")
	if got["amount"] != "120.50" || got["updated_at"] != nil {
		t.Errorf("record changed by failed PUT: %v", got)
	}
}

func TestPatchPartialUpdate(t *testing.T) {
	h := newTestServer(t)
	uid := seedPayable(t, h, "KBank debit", "THB")
	path, orig := create(t, h, `{"amount":"120.50","currency":"THB","balance_uid":"`+uid+`","spent_at":"2026-10-05T09:00:00+07:00","note":"lunch"}`)

	rec, body := do(t, h, "PATCH", path, `{"note":"team lunch"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body)
	}
	for _, k := range []string{"amount", "amount_minor", "currency", "balance_uid", "account", "spent_at", "created_at"} {
		if body[k] != orig[k] {
			t.Errorf("%s changed: %v -> %v", k, orig[k], body[k])
		}
	}
	if body["note"] != "team lunch" || body["updated_at"] == nil {
		t.Errorf("unexpected patch body: %v", body)
	}

	_, body = do(t, h, "PATCH", path, `{"spent_at":"2026-10-05T11:15:00+08:00"}`)
	if body["spent_at"] != "2026-10-05T11:15:00+08:00" || body["note"] != "team lunch" {
		t.Errorf("spent_at patch: %v", body)
	}

	_, body = do(t, h, "PATCH", path, `{"note":null}`)
	if body["note"] != "" {
		t.Errorf("note null: %v", body)
	}

	cash := seedPayable(t, h, "Cash", "THB")
	rec, body = do(t, h, "PATCH", path, `{"balance_uid":"`+cash+`"}`)
	if rec.Code != 200 || body["balance_uid"] != cash || body["account"] != "Cash" {
		t.Errorf("balance_uid patch: %d %v", rec.Code, body)
	}
}

func TestPatchCurrencyChange(t *testing.T) {
	h := newTestServer(t)
	uid := seedPayable(t, h, "Cash", "THB")
	jpy := seedPayable(t, h, "Yen wallet", "JPY")
	kwd := seedPayable(t, h, "Dinar wallet", "KWD")
	path, _ := create(t, h, `{"amount":"120.50","currency":"THB","balance_uid":"`+uid+`","spent_at":"2026-10-05T09:00:00+07:00","note":"lunch"}`)

	// The transaction currency must match its balance (no FX), so a currency
	// change goes together with a balance in that currency.
	rec, body := do(t, h, "PATCH", path, `{"currency":"JPY","amount":"1500"}`)
	if rec.Code != http.StatusUnprocessableEntity || body["fields"].(map[string]any)["balance_uid"] == nil {
		t.Fatalf("THB->JPY on a THB balance: %d %s", rec.Code, rec.Body)
	}

	rec, body = do(t, h, "PATCH", path, `{"currency":"JPY","balance_uid":"`+jpy+`"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("THB->JPY: %d %s", rec.Code, rec.Body)
	}
	if f, _ := body["fields"].(map[string]any); f["amount"] == nil {
		t.Errorf("want amount field error, got %v", body)
	}

	rec, body = do(t, h, "PATCH", path, `{"currency":"JPY","amount":"1500","balance_uid":"`+jpy+`"}`)
	if rec.Code != http.StatusOK || body["amount"] != "1500" || body["amount_minor"].(float64) != 1500 {
		t.Errorf("JPY with amount: %d %v", rec.Code, body)
	}

	rec, body = do(t, h, "PATCH", path, `{"currency":"kwd","balance_uid":"`+kwd+`"}`)
	if rec.Code != http.StatusOK || body["amount"] != "1500.000" || body["amount_minor"].(float64) != 1500000 {
		t.Errorf("JPY->KWD: %d %v", rec.Code, body)
	}

	rec, body = do(t, h, "PATCH", path, `{"currency":"JPY","balance_uid":"`+jpy+`"}`)
	if rec.Code != http.StatusOK || body["amount"] != "1500" {
		t.Errorf("KWD->JPY: %d %v", rec.Code, body)
	}
}

func TestPatchErrors(t *testing.T) {
	h := newTestServer(t)
	uid := seedPayable(t, h, "Cash", "THB")
	path, _ := create(t, h, `{"amount":"120.50","currency":"THB","balance_uid":"`+uid+`","spent_at":"2026-10-05T09:00:00+07:00","note":"lunch"}`)
	cases := []struct {
		name, path, body string
		status           int
	}{
		{"missing id", "/api/transactions/00000000-0000-4000-8000-000000009999", `{"note":"x"}`, 404},
		{"bad id", "/api/transactions/1", `{"note":"x"}`, 400},
		{"empty object", path, `{}`, 400},
		{"null body", path, `null`, 400},
		{"array body", path, `[1]`, 400},
		{"unknown field", path, `{"tip":"5"}`, 400},
		{"legacy account", path, `{"account":"Cash"}`, 400},
		{"wrong type", path, `{"balance_uid":5}`, 400},
		{"null amount", path, `{"amount":null}`, 422},
		{"bad amount", path, `{"amount":"-3"}`, 422},
		{"blank balance_uid", path, `{"balance_uid":"   "}`, 422},
		{"no offset", path, `{"spent_at":"2026-10-05T09:00:00"}`, 422},
		{"malformed", path, `{"note":`, 400},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec, body := do(t, h, "PATCH", c.path, c.body)
			if rec.Code != c.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, c.status, rec.Body)
			}
			if body["error"] == nil {
				t.Errorf("missing error message: %v", body)
			}
		})
	}
	_, got := do(t, h, "GET", path, "")
	if got["updated_at"] != nil || got["note"] != "lunch" {
		t.Errorf("record changed by failed PATCHes: %v", got)
	}
}
