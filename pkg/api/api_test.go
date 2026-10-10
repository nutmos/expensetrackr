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

	"github.com/nutmos/expensetrackr/pkg/store"

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

// do sends a request. For PUT on a single balance or transaction whose
// body has no "version", it first GETs the record and sends its ETag as
// If-Match, like a well-behaved client, so tests that are not about
// versioning need not handle it. Use doH to control headers exactly.
func do(t *testing.T, h http.Handler, method, path, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var hdr map[string]string
	if method == "PUT" && !strings.Contains(body, `"version"`) &&
		(strings.HasPrefix(path, "/api/balances/") || strings.HasPrefix(path, "/api/transactions/")) {
		if rec, _ := doH(t, h, "GET", path, "", nil); rec.Code == http.StatusOK && rec.Header().Get("ETag") != "" {
			hdr = map[string]string{"If-Match": rec.Header().Get("ETag")}
		}
	}
	return doH(t, h, method, path, body, hdr)
}

// doH sends a request with exactly the given extra headers.
func doH(t *testing.T, h http.Handler, method, path, body string, hdr map[string]string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
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
	// Only UUIDs are accepted: malformed (incl. numeric ids) -> 400, unknown -> 404.
	for _, res := range []string{"transactions", "balances"} {
		for _, bad := range []string{"abc", "0", "1", "12345"} {
			if rec, _ := do(t, h, "GET", "/api/"+res+"/"+bad, ""); rec.Code != http.StatusBadRequest {
				t.Errorf("%s/%s: status %d, want 400", res, bad, rec.Code)
			}
		}
		if rec, _ := do(t, h, "DELETE", "/api/"+res+"/7", ""); rec.Code != http.StatusBadRequest {
			t.Errorf("DELETE %s/7: status %d, want 400", res, rec.Code)
		}
		if rec, _ := do(t, h, "GET", "/api/"+res+"/AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA", ""); rec.Code != http.StatusNotFound {
			t.Errorf("%s unknown uid: status %d, want 404", res, rec.Code)
		}
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

// editableFields lists the fields a PUT body carries, per resource prefix.
var editableFields = map[string][]string{
	"/api/transactions/": {"amount", "currency", "type", "balance_uid", "to_balance_uid", "category_uid", "spent_at", "note"},
	"/api/balances/":     {"name", "type", "currency", "description", "balance", "debt", "limit"},
	"/api/categories/":   {"name", "type", "description"},
}

// fullBody GETs the record at path and returns a full PUT body: its editable
// fields with the JSON object changes overlaid (a null in changes omits that
// field, which PUT treats as empty/absent). This is how a client edits only
// some fields now that there is no PATCH: it sends the whole record.
func fullBody(t *testing.T, h http.Handler, path, changes string) string {
	t.Helper()
	rec, cur := doH(t, h, "GET", path, "", nil)
	if rec.Code != http.StatusOK {
		return changes // let the PUT report the 400/404
	}
	var keys []string
	for prefix, k := range editableFields {
		if strings.HasPrefix(path, prefix) {
			keys = k
		}
	}
	body := map[string]any{}
	for _, k := range keys {
		if v, ok := cur[k]; ok && v != nil {
			body[k] = v
		}
	}
	var ch map[string]any
	if err := json.Unmarshal([]byte(changes), &ch); err != nil {
		t.Fatalf("changes %q: %v", changes, err)
	}
	for k, v := range ch {
		if v == nil {
			delete(body, k)
		} else {
			body[k] = v
		}
	}
	b, _ := json.Marshal(body)
	return string(b)
}

// putMerged PUTs the record at path with changes applied (see fullBody),
// sending If-Match like do.
func putMerged(t *testing.T, h http.Handler, path, changes string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	return do(t, h, "PUT", path, fullBody(t, h, path, changes))
}

// putMergedH is putMerged with exactly the given headers (see doH).
func putMergedH(t *testing.T, h http.Handler, path, changes string, hdr map[string]string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	return doH(t, h, "PUT", path, fullBody(t, h, path, changes), hdr)
}
