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

// seedPayable creates a payment_account balance and returns its uid.
func seedPayable(t *testing.T, h http.Handler, name, currency string) string {
	t.Helper()
	rec, body := do(t, h, "POST", "/api/balances",
		`{"name":`+strconv.Quote(name)+`,"type":"payment_account","currency":`+strconv.Quote(currency)+`,"balance":"10000"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("seed balance: %d %s", rec.Code, rec.Body)
	}
	uid, _ := body["uid"].(string)
	if uid == "" {
		t.Fatal("missing uid")
	}
	return uid
}

func seedCard(t *testing.T, h http.Handler, name, currency string) string {
	t.Helper()
	rec, body := do(t, h, "POST", "/api/balances",
		`{"name":`+strconv.Quote(name)+`,"type":"credit_card","currency":`+strconv.Quote(currency)+`,"debt":"0","limit":"50000"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("seed card: %d %s", rec.Code, rec.Body)
	}
	return body["uid"].(string)
}

func TestCreateGetDelete(t *testing.T) {
	h := newTestServer(t)
	uid := seedPayable(t, h, "KBank debit", "THB")

	rec, body := do(t, h, "POST", "/api/expenses",
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
	id := int(body["id"].(float64))
	if loc := rec.Header().Get("Location"); loc == "" {
		t.Errorf("missing Location header")
	}

	rec, body = do(t, h, "GET", "/api/expenses/"+itoa(id), "")
	if rec.Code != http.StatusOK || body["account"] != "KBank debit" || body["balance_uid"] != uid || body["note"] != "lunch" {
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
	cash := seedPayable(t, h, "Cash", "THB")
	card := seedCard(t, h, "Card", "USD")
	inputs := []string{
		`{"amount":"1","currency":"THB","balance_uid":"` + cash + `","spent_at":"2026-10-05T09:00:00+07:00"}`,
		`{"amount":"2","currency":"SGD","balance_uid":"` + cash + `","spent_at":"2026-10-05T12:00:00+08:00"}`,
		`{"amount":"3","currency":"USD","balance_uid":"` + card + `","spent_at":"2026-10-05T03:00:00Z"}`,
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

	q := url.Values{"from": {"2026-10-05T10:30:00+08:00"}, "to": {"2026-10-05T11:30:00+08:00"}}
	rec, body = do(t, h, "GET", "/api/expenses?"+q.Encode(), "")
	if rec.Code != http.StatusOK || strings.Join(amounts(body), ",") != "3.00" {
		t.Errorf("filtered = %d %v, want [3.00]", rec.Code, amounts(body))
	}
}

func TestBadIDAndIndex(t *testing.T) {
	h := newTestServer(t)
	// Non-digit segments are treated as a uid: unknown -> 404.
	if rec, _ := do(t, h, "GET", "/api/expenses/abc", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown uid: status %d", rec.Code)
	}
	if rec, _ := do(t, h, "GET", "/api/expenses/0", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("bad id 0: status %d", rec.Code)
	}
	rec, _ := do(t, h, "GET", "/", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Expense Log") {
		t.Errorf("index: status %d body %q", rec.Code, rec.Body)
	}
}

func TestPayableBalancesFilter(t *testing.T) {
	h := newTestServer(t)
	seedPayable(t, h, "Cash", "THB")
	seedCard(t, h, "Visa", "SGD")
	do(t, h, "POST", "/api/balances", `{"name":"Gold","type":"other_asset","currency":"THB","balance":"1"}`)
	rec, body := do(t, h, "GET", "/api/balances?payable=1", "")
	if rec.Code != 200 || body["count"].(float64) != 2 {
		t.Fatalf("payable: %d %v", rec.Code, body)
	}
	for _, it := range body["balances"].([]any) {
		typ := it.(map[string]any)["type"].(string)
		if typ != "payment_account" && typ != "credit_card" {
			t.Errorf("non-payable in list: %s", typ)
		}
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

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}
