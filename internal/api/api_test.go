package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestBadIDAndIndex(t *testing.T) {
	h := newTestServer(t)
	// Non-digit segments are treated as a uid: unknown -> 404.
	if rec, _ := do(t, h, "GET", "/api/transactions/abc", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown uid: status %d", rec.Code)
	}
	if rec, _ := do(t, h, "GET", "/api/transactions/0", ""); rec.Code != http.StatusBadRequest {
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
