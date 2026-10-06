package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"expense-service/internal/store"

	"github.com/gin-gonic/gin"
)

func newTestServer(t *testing.T) http.Handler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	static := fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>Expense Log</title>")}}
	return (&Server{Store: st, Static: static}).Router()
}

func do(t *testing.T, h http.Handler, method, path, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var m map[string]any
	if rec.Body.Len() > 0 && strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatalf("decode response %q: %v", rec.Body.String(), err)
		}
	}
	return rec, m
}

func TestCreateGetDelete(t *testing.T) {
	h := newTestServer(t)

	rec, body := do(t, h, "POST", "/api/expenses",
		`{"amount":"120.5","currency":"thb","account":"KBank debit","spent_at":"2026-10-06T21:06:00+08:00","note":"lunch"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status %d body %s", rec.Code, rec.Body)
	}
	if body["amount"] != "120.50" || body["currency"] != "THB" || body["amount_minor"].(float64) != 12050 {
		t.Errorf("unexpected create body: %v", body)
	}
	if body["spent_at"] != "2026-10-06T21:06:00+08:00" {
		t.Errorf("spent_at not preserved: %v", body["spent_at"])
	}
	id := int(body["id"].(float64))
	if loc := rec.Header().Get("Location"); loc == "" {
		t.Errorf("missing Location header")
	}

	rec, body = do(t, h, "GET", "/api/expenses/"+itoa(id), "")
	if rec.Code != http.StatusOK || body["account"] != "KBank debit" || body["note"] != "lunch" {
		t.Fatalf("get: status %d body %v", rec.Code, body)
	}

	rec, _ = do(t, h, "DELETE", "/api/expenses/"+itoa(id), "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: status %d", rec.Code)
	}
	rec, _ = do(t, h, "GET", "/api/expenses/"+itoa(id), "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete: status %d", rec.Code)
	}
	rec, _ = do(t, h, "DELETE", "/api/expenses/"+itoa(id), "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("second delete: status %d", rec.Code)
	}
}

func TestCreateValidationErrors(t *testing.T) {
	h := newTestServer(t)
	cases := []struct {
		name, body string
		status     int
		field      string
	}{
		{"missing offset", `{"amount":"10","currency":"SGD","account":"Cash","spent_at":"2026-10-06T21:06:00"}`, 422, "spent_at"},
		{"bad currency", `{"amount":"10","currency":"ABC","account":"Cash","spent_at":"2026-10-06T21:06:00+08:00"}`, 422, "currency"},
		{"negative amount", `{"amount":"-10","currency":"USD","account":"Cash","spent_at":"2026-10-06T21:06:00+08:00"}`, 422, "amount"},
		{"too many decimals", `{"amount":"1.005","currency":"USD","account":"Cash","spent_at":"2026-10-06T21:06:00+08:00"}`, 422, "amount"},
		{"missing account", `{"amount":"10","currency":"USD","spent_at":"2026-10-06T21:06:00+08:00"}`, 422, "account"},
		{"malformed json", `{"amount":`, 400, ""},
		{"unknown field", `{"amount":"10","currency":"USD","account":"Cash","spent_at":"2026-10-06T21:06:00+08:00","tip":1}`, 400, ""},
		{"empty body", ``, 400, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec, body := do(t, h, "POST", "/api/expenses", c.body)
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
	// Same calendar day, different offsets: ordering must follow the real instant.
	inputs := []string{
		`{"amount":"1","currency":"THB","account":"Cash","spent_at":"2026-10-05T09:00:00+07:00"}`, // 02:00Z
		`{"amount":"2","currency":"SGD","account":"Cash","spent_at":"2026-10-05T12:00:00+08:00"}`, // 04:00Z
		`{"amount":"3","currency":"USD","account":"Card","spent_at":"2026-10-05T03:00:00Z"}`,      // 03:00Z
	}
	for _, in := range inputs {
		if rec, _ := do(t, h, "POST", "/api/expenses", in); rec.Code != http.StatusCreated {
			t.Fatalf("seed: %d %s", rec.Code, rec.Body)
		}
	}

	rec, body := do(t, h, "GET", "/api/expenses", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	got := amounts(body)
	if strings.Join(got, ",") != "2.00,3.00,1.00" {
		t.Errorf("order = %v, want newest first [2.00 3.00 1.00]", got)
	}

	q := url.Values{"from": {"2026-10-05T10:30:00+08:00"}, "to": {"2026-10-05T11:30:00+08:00"}} // 02:30Z..03:30Z
	rec, body = do(t, h, "GET", "/api/expenses?"+q.Encode(), "")
	if rec.Code != http.StatusOK || strings.Join(amounts(body), ",") != "3.00" {
		t.Errorf("filtered = %d %v, want [3.00]", rec.Code, amounts(body))
	}

	// Unencoded '+' (decoded as space) is tolerated.
	rec, body = do(t, h, "GET", "/api/expenses?from=2026-10-05T11:00:00+08:00", "")
	if rec.Code != http.StatusOK || strings.Join(amounts(body), ",") != "2.00,3.00" {
		t.Errorf("unencoded plus = %d %v, want [2.00 3.00]", rec.Code, amounts(body))
	}

	rec, body = do(t, h, "GET", "/api/expenses?from=yesterday", "")
	if rec.Code != http.StatusBadRequest || body["fields"] == nil {
		t.Errorf("bad filter: status %d body %v", rec.Code, body)
	}
	rec, _ = do(t, h, "GET", "/api/expenses?from=2026-10-06T00:00:00Z&to=2026-10-05T00:00:00Z", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("from > to: status %d, want 400", rec.Code)
	}
}

func TestBadIDAndIndex(t *testing.T) {
	h := newTestServer(t)
	if rec, _ := do(t, h, "GET", "/api/expenses/abc", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("bad id: status %d", rec.Code)
	}
	rec, _ := do(t, h, "GET", "/", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Expense Log") {
		t.Errorf("index: status %d body %q", rec.Code, rec.Body)
	}
}

func amounts(body map[string]any) []string {
	var out []string
	items, _ := body["expenses"].([]any)
	for _, it := range items {
		out = append(out, it.(map[string]any)["amount"].(string))
	}
	return out
}

func itoa(i int) string { return strconv.Itoa(i) }
