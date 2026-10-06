package api

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
)

const seedTHB = `{"amount":"120.50","currency":"THB","account":"KBank debit","spent_at":"2026-10-05T09:00:00+07:00","note":"lunch"}`

func create(t *testing.T, h http.Handler, body string) (string, map[string]any) {
	t.Helper()
	rec, m := do(t, h, "POST", "/api/expenses", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("seed: %d %s", rec.Code, rec.Body)
	}
	return "/api/expenses/" + strconv.Itoa(int(m["id"].(float64))), m
}

func TestPutReplacesExpense(t *testing.T) {
	h := newTestServer(t)
	path, orig := create(t, h, seedTHB)
	if orig["updated_at"] != nil {
		t.Errorf("new expense should have updated_at null, got %v", orig["updated_at"])
	}

	// Note omitted: a full replace clears it.
	rec, body := do(t, h, "PUT", path, `{"amount":"18.9","currency":"sgd","account":"Cash","spent_at":"2026-10-06T12:30:00+08:00"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	if body["amount"] != "18.90" || body["currency"] != "SGD" || body["account"] != "Cash" ||
		body["spent_at"] != "2026-10-06T12:30:00+08:00" || body["note"] != "" {
		t.Errorf("unexpected put body: %v", body)
	}
	if body["id"] != orig["id"] || body["created_at"] != orig["created_at"] {
		t.Errorf("id/created_at must not change: %v vs %v", body, orig)
	}
	if s, _ := body["updated_at"].(string); !strings.HasSuffix(s, "Z") {
		t.Errorf("updated_at should be UTC RFC 3339, got %v", body["updated_at"])
	}

	// Persisted.
	_, got := do(t, h, "GET", path, "")
	if got["amount"] != "18.90" || got["updated_at"] != body["updated_at"] {
		t.Errorf("GET after PUT: %v", got)
	}
}

func TestPutErrors(t *testing.T) {
	h := newTestServer(t)
	path, _ := create(t, h, seedTHB)
	valid := `{"amount":"1","currency":"USD","account":"Cash","spent_at":"2026-10-06T12:30:00Z"}`
	cases := []struct {
		name, path, body string
		status           int
		field            string
	}{
		{"missing id", "/api/expenses/9999", valid, 404, ""},
		{"missing id + invalid body", "/api/expenses/9999", `{"amount":"x"}`, 404, ""},
		{"bad id", "/api/expenses/abc", valid, 400, ""},
		{"no offset", path, `{"amount":"1","currency":"USD","account":"Cash","spent_at":"2026-10-06T12:30:00"}`, 422, "spent_at"},
		{"missing fields", path, `{"amount":"1"}`, 422, "currency"},
		{"unknown field", path, `{"amount":"1","currency":"USD","account":"Cash","spent_at":"2026-10-06T12:30:00Z","id":5}`, 400, ""},
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
	// Failed PUTs must not modify the record.
	_, got := do(t, h, "GET", path, "")
	if got["amount"] != "120.50" || got["updated_at"] != nil {
		t.Errorf("record changed by failed PUT: %v", got)
	}
}

func TestPatchPartialUpdate(t *testing.T) {
	h := newTestServer(t)
	path, orig := create(t, h, seedTHB)

	rec, body := do(t, h, "PATCH", path, `{"note":"team lunch"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body)
	}
	for _, k := range []string{"amount", "amount_minor", "currency", "account", "spent_at", "created_at"} {
		if body[k] != orig[k] {
			t.Errorf("%s changed: %v -> %v", k, orig[k], body[k])
		}
	}
	if body["note"] != "team lunch" || body["updated_at"] == nil {
		t.Errorf("unexpected patch body: %v", body)
	}

	// Changing only spent_at keeps the supplied offset.
	_, body = do(t, h, "PATCH", path, `{"spent_at":"2026-10-05T11:15:00+08:00"}`)
	if body["spent_at"] != "2026-10-05T11:15:00+08:00" || body["note"] != "team lunch" {
		t.Errorf("spent_at patch: %v", body)
	}

	// note:null clears the note.
	_, body = do(t, h, "PATCH", path, `{"note":null}`)
	if body["note"] != "" {
		t.Errorf("note null: %v", body)
	}
}

func TestPatchCurrencyChange(t *testing.T) {
	h := newTestServer(t)

	// THB 120.50 -> JPY alone is rejected (JPY has no minor unit) and nothing changes.
	path, _ := create(t, h, seedTHB)
	rec, body := do(t, h, "PATCH", path, `{"currency":"JPY"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("THB->JPY: %d %s", rec.Code, rec.Body)
	}
	if f, _ := body["fields"].(map[string]any); f["amount"] == nil {
		t.Errorf("want amount field error, got %v", body)
	}
	_, got := do(t, h, "GET", path, "")
	if got["currency"] != "THB" || got["updated_at"] != nil {
		t.Errorf("record changed by rejected patch: %v", got)
	}

	// Currency + amount together works.
	rec, body = do(t, h, "PATCH", path, `{"currency":"JPY","amount":"1500"}`)
	if rec.Code != http.StatusOK || body["amount"] != "1500" || body["amount_minor"].(float64) != 1500 {
		t.Errorf("JPY with amount: %d %v", rec.Code, body)
	}

	// JPY 1500 -> KWD (3 decimals) rescales minor units: 1500 -> 1500000.
	rec, body = do(t, h, "PATCH", path, `{"currency":"kwd"}`)
	if rec.Code != http.StatusOK || body["amount"] != "1500.000" || body["amount_minor"].(float64) != 1500000 {
		t.Errorf("JPY->KWD: %d %v", rec.Code, body)
	}

	// KWD 1500.000 -> JPY works because the extra zeros carry no value.
	rec, body = do(t, h, "PATCH", path, `{"currency":"JPY"}`)
	if rec.Code != http.StatusOK || body["amount"] != "1500" {
		t.Errorf("KWD->JPY: %d %v", rec.Code, body)
	}

	// Unknown currency.
	rec, body = do(t, h, "PATCH", path, `{"currency":"XYZ"}`)
	if f, _ := body["fields"].(map[string]any); rec.Code != 422 || f["currency"] == nil {
		t.Errorf("XYZ: %d %v", rec.Code, body)
	}
}

func TestPatchErrors(t *testing.T) {
	h := newTestServer(t)
	path, _ := create(t, h, seedTHB)
	cases := []struct {
		name, path, body string
		status           int
	}{
		{"missing id", "/api/expenses/9999", `{"note":"x"}`, 404},
		{"bad id", "/api/expenses/0", `{"note":"x"}`, 400},
		{"empty object", path, `{}`, 400},
		{"null body", path, `null`, 400},
		{"array body", path, `[1]`, 400},
		{"unknown field", path, `{"tip":"5"}`, 400},
		{"wrong type", path, `{"account":5}`, 400},
		{"null amount", path, `{"amount":null}`, 422},
		{"bad amount", path, `{"amount":"-3"}`, 422},
		{"blank account", path, `{"account":"   "}`, 422},
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
