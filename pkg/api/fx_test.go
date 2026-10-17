package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/nutmos/expensetrackr/pkg/fx"
	"github.com/nutmos/expensetrackr/pkg/store"

	"github.com/gin-gonic/gin"
)

// fakeFrankfurter serves a few fixed rates like api.frankfurter.dev/v1. A
// request for a Saturday answers with Friday's rate (as the real API does);
// VND is unsupported (404).
func fakeFrankfurter(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	rates := map[string]float64{"USD/THB": 33.615, "USD/JPY": 149.567, "THB/USD": 0.02975, "SGD/THB": 25.9}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
		date := strings.TrimPrefix(r.URL.Path, "/")
		switch date {
		case "latest":
			date = "2026-10-12"
		case "2026-10-10", "2026-10-11":
			date = "2026-10-09"
		}
		rate, ok := rates[from+"/"+to]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"not found"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"amount": 1.0, "base": from, "date": date, "rates": map[string]float64{to: rate}})
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

func newFXServer(t *testing.T, fxURL string) (http.Handler, *store.Store) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	st, err := store.Open(filepath.Join(t.TempDir(), "fx.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	static := fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}}
	client := &fx.Client{BaseURL: fxURL, Cache: st, HTTP: &http.Client{Timeout: 2 * time.Second},
		Now: func() time.Time { return time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC) }}
	return loggedIn(t, (&Server{Store: st, Static: static, FX: client}).Router(), st), st
}

func balanceOf(t *testing.T, h http.Handler, uid string) string {
	t.Helper()
	_, b := do(t, h, "GET", "/api/balances/"+uid, "")
	s, _ := b["balance"].(string)
	return s
}

func transferBody(from, to, amount, currency, at, extra string) string {
	return `{"type":"transfer","amount":"` + amount + `","currency":"` + currency + `","balance_uid":"` + from +
		`","to_balance_uid":"` + to + `","spent_at":"` + at + `"` + extra + `}`
}

func TestCrossCurrencyTransferAuto(t *testing.T) {
	fake, hits := fakeFrankfurter(t)
	h, _ := newFXServer(t, fake.URL)
	usd := seedPayable(t, h, "Dollars", "USD")
	thb := seedPayable(t, h, "Baht", "THB")

	// Saturday -> Friday's rate; to_amount = 100 * 33.615.
	rec, b := do(t, h, "POST", "/api/transactions", transferBody(usd, thb, "100", "USD", "2026-10-10T09:00:00+07:00", ""))
	if rec.Code != 201 {
		t.Fatalf("create: %d %v", rec.Code, b)
	}
	want := map[string]any{"amount": "100.00", "currency": "USD", "to_amount": "3361.50", "to_currency": "THB",
		"exchange_rate": "33.615", "rate_source": "frankfurter", "rate_date": "2026-10-09", "to_amount_minor": float64(336150)}
	for k, v := range want {
		if b[k] != v {
			t.Errorf("%s = %v, want %v", k, b[k], v)
		}
	}
	if got := balanceOf(t, h, usd); got != "9900.00" {
		t.Errorf("USD balance %s", got)
	}
	if got := balanceOf(t, h, thb); got != "13361.50" {
		t.Errorf("THB balance %s", got)
	}
	if hits.Load() != 1 {
		t.Errorf("hits %d", hits.Load())
	}
	// Same pair and date again: served from the fx_rates cache.
	if rec, b := do(t, h, "POST", "/api/transactions", transferBody(usd, thb, "1", "USD", "2026-10-10T20:00:00+07:00", "")); rec.Code != 201 || b["to_amount"] != "33.62" {
		t.Fatalf("second: %d %v", rec.Code, b)
	}
	if rec, _ := do(t, h, "GET", "/api/exchange-rates?from=USD&to=THB&date=2026-10-10", ""); rec.Code != 200 {
		t.Fatalf("preview: %d", rec.Code)
	}
	if hits.Load() != 1 {
		t.Errorf("cache miss: hits %d, want 1", hits.Load())
	}
	// Exchange-rate endpoint: latest (date omitted / today / future).
	for _, q := range []string{"", "&date=2026-10-13", "&date=2027-01-01"} {
		rec, b := do(t, h, "GET", "/api/exchange-rates?from=usd&to=THB"+q, "")
		if rec.Code != 200 || b["rate"] != "33.615" || b["date"] != "2026-10-12" || b["source"] != "frankfurter" || b["from"] != "USD" {
			t.Errorf("latest%s: %d %v", q, rec.Code, b)
		}
	}
	if hits.Load() != 2 { // one 'latest' fetch, then cached
		t.Errorf("latest hits %d, want 2", hits.Load())
	}
	if rec, b := do(t, h, "GET", "/api/exchange-rates?from=THB&to=THB", ""); rec.Code != 200 || b["rate"] != "1" {
		t.Errorf("same currency: %d %v", rec.Code, b)
	}
	if rec, _ := do(t, h, "GET", "/api/exchange-rates?from=USD", ""); rec.Code != 400 {
		t.Errorf("missing to: %d", rec.Code)
	}
	if rec, _ := do(t, h, "GET", "/api/exchange-rates?from=USD&to=THB&date=10/10/2026", ""); rec.Code != 400 {
		t.Errorf("bad date: %d", rec.Code)
	}
}

func TestCrossCurrencyTransferManualEditDelete(t *testing.T) {
	fake, hits := fakeFrankfurter(t)
	h, _ := newFXServer(t, fake.URL)
	usd := seedPayable(t, h, "Dollars", "USD")
	thb := seedPayable(t, h, "Baht", "THB")
	card := seedCard(t, h, "Card", "THB") // debt 0 per seedCard

	rec, b := do(t, h, "POST", "/api/transactions", transferBody(usd, thb, "100", "USD", "2026-10-09T09:00:00+07:00", `,"to_amount":"3300"`))
	if rec.Code != 201 || b["rate_source"] != "manual" || b["exchange_rate"] != "33" || b["to_amount"] != "3300.00" || b["rate_date"] != nil {
		t.Fatalf("manual: %d %v", rec.Code, b)
	}
	if hits.Load() != 0 {
		t.Errorf("manual fetched a rate")
	}
	path := "/api/transactions/" + b["uid"].(string)
	if balanceOf(t, h, thb) != "13300.00" || balanceOf(t, h, usd) != "9900.00" {
		t.Fatalf("after manual: %s %s", balanceOf(t, h, usd), balanceOf(t, h, thb))
	}

	// Note-only edit without to_amount: stored conversion kept, no fetch.
	rec, b = do(t, h, "PUT", path, transferBody(usd, thb, "100", "USD", "2026-10-09T10:00:00+07:00", `,"note":"x"`))
	if rec.Code != 200 || b["rate_source"] != "manual" || b["to_amount"] != "3300.00" || hits.Load() != 0 {
		t.Fatalf("note edit: %d %v hits=%d", rec.Code, b, hits.Load())
	}
	// Amount change without to_amount: refetch (Friday rate).
	rec, b = do(t, h, "PUT", path, transferBody(usd, thb, "50", "USD", "2026-10-09T10:00:00+07:00", ""))
	if rec.Code != 200 || b["rate_source"] != "frankfurter" || b["to_amount"] != "1680.75" || b["rate_date"] != "2026-10-09" {
		t.Fatalf("refetch: %d %v", rec.Code, b)
	}
	if balanceOf(t, h, thb) != "11680.75" || balanceOf(t, h, usd) != "9950.00" {
		t.Errorf("after edit: %s %s", balanceOf(t, h, usd), balanceOf(t, h, thb))
	}
	// Switch destination to a THB credit card: payment lowers its debt by
	// to_amount; the old destination is reversed by its stored to_amount.
	rec, b = do(t, h, "PUT", path, transferBody(usd, card, "10", "USD", "2026-10-09T10:00:00+07:00", `,"to_amount":"336"`))
	if rec.Code != 200 || b["to_amount"] != "336.00" || b["exchange_rate"] != "33.6" {
		t.Fatalf("to card: %d %v", rec.Code, b)
	}
	if balanceOf(t, h, thb) != "10000.00" || balanceOf(t, h, usd) != "9990.00" {
		t.Errorf("after switch: %s %s", balanceOf(t, h, usd), balanceOf(t, h, thb))
	}
	_, cb := do(t, h, "GET", "/api/balances/"+card, "")
	if cb["debt"] != "-336.00" {
		t.Errorf("card debt %v, want -336.00", cb["debt"])
	}
	// Delete: reversed with the stored values even though rates "changed".
	if rec, _ := do(t, h, "DELETE", path, ""); rec.Code != 204 {
		t.Fatalf("delete: %d", rec.Code)
	}
	if balanceOf(t, h, usd) != "10000.00" {
		t.Errorf("usd after delete %s", balanceOf(t, h, usd))
	}
	_, cb = do(t, h, "GET", "/api/balances/"+card, "")
	if cb["debt"] != "0.00" {
		t.Errorf("card debt not reversed: %v", cb["debt"])
	}

	// Editing into a same-currency transfer clears the to_* fields.
	path, _ = create(t, h, transferBody(usd, thb, "1", "USD", "2026-10-09T09:00:00+07:00", `,"to_amount":"33"`))
	usd2 := seedPayable(t, h, "Dollars 2", "USD")
	rec, b = do(t, h, "PUT", path, transferBody(usd, usd2, "1", "USD", "2026-10-09T09:00:00+07:00", ""))
	if rec.Code != 200 || b["to_amount"] != nil || b["exchange_rate"] != nil || b["rate_source"] != nil {
		t.Errorf("to same currency: %d %v", rec.Code, b)
	}
	if balanceOf(t, h, thb) != "10000.00" || balanceOf(t, h, usd2) != "10001.00" || balanceOf(t, h, usd) != "9999.00" {
		t.Errorf("after same-currency edit: %s %s %s", balanceOf(t, h, usd), balanceOf(t, h, usd2), balanceOf(t, h, thb))
	}
}

func TestCrossCurrencyRoundingJPY(t *testing.T) {
	fake, _ := fakeFrankfurter(t)
	h, _ := newFXServer(t, fake.URL)
	usd := seedPayable(t, h, "Dollars", "USD")
	jpy := seedPayable(t, h, "Yen", "JPY")
	// 1.01 * 149.567 = 151.06267 -> 151; 0.03 * 149.567 = 4.48701 -> 4; 2.51*149.567=375.41317 -> 375
	for amt, want := range map[string]string{"1.01": "151", "0.03": "4", "10.03": "1500", "0.01": "1"} {
		rec, b := do(t, h, "POST", "/api/transactions", transferBody(usd, jpy, amt, "USD", "2026-10-09T09:00:00Z", ""))
		if rec.Code != 201 || b["to_amount"] != want {
			t.Errorf("%s USD -> %v JPY (%d), want %s", amt, b["to_amount"], rec.Code, want)
		}
	}
	// Manual JPY amounts use JPY's scale.
	if rec, b := do(t, h, "POST", "/api/transactions", transferBody(usd, jpy, "1", "USD", "2026-10-09T09:00:00Z", `,"to_amount":"150.5"`)); rec.Code != 422 || fieldsOf(b)["to_amount"] == nil {
		t.Errorf("JPY decimals: %d %v", rec.Code, b)
	}
}

func TestCrossCurrencyErrors(t *testing.T) {
	fake, _ := fakeFrankfurter(t)
	h, _ := newFXServer(t, fake.URL)
	usd := seedPayable(t, h, "Dollars", "USD")
	usd2 := seedPayable(t, h, "Dollars 2", "USD")
	thb := seedPayable(t, h, "Baht", "THB")
	vnd := seedPayable(t, h, "Dong", "VND")
	gold := seedBal(t, h, "Gold", "other_asset") // THB

	at := "2026-10-09T09:00:00Z"
	cases := []struct {
		name, body string
		code       int
		field      string
	}{
		{"unsupported pair", transferBody(usd, vnd, "10", "USD", at, ""), 422, "to_amount"},
		{"same currency mismatched to_amount", transferBody(usd, usd2, "10", "USD", at, `,"to_amount":"11"`), 422, "to_amount"},
		{"wrong to_currency", transferBody(usd, thb, "10", "USD", at, `,"to_currency":"EUR"`), 422, "to_currency"},
		{"currency not source's", transferBody(usd, thb, "10", "THB", at, ""), 422, "balance_uid"},
		{"to_amount on expense", `{"amount":"1","currency":"USD","balance_uid":"` + usd + `","spent_at":"` + at + `","to_amount":"1"}`, 422, "to_amount"},
		{"asset purchase cross-currency", `{"type":"asset_purchase","amount":"1","currency":"USD","balance_uid":"` + usd + `","to_balance_uid":"` + gold + `","spent_at":"` + at + `"}`, 422, "to_balance_uid"},
		{"zero to_amount", transferBody(usd, thb, "10", "USD", at, `,"to_amount":"0"`), 422, "to_amount"},
	}
	for _, tc := range cases {
		rec, b := do(t, h, "POST", "/api/transactions", tc.body)
		if rec.Code != tc.code || fieldsOf(b)[tc.field] == nil {
			t.Errorf("%s: %d %v", tc.name, rec.Code, b)
		}
	}
	if rec, b := do(t, h, "POST", "/api/transactions", transferBody(usd, vnd, "10", "USD", at, "")); !strings.Contains(b["error"].(string), "to_amount") || b["code"] != "exchange_rate_unsupported" {
		t.Errorf("unsupported message: %d %v", rec.Code, b)
	}
	// Manual works for the unsupported pair.
	if rec, b := do(t, h, "POST", "/api/transactions", transferBody(usd, vnd, "10", "USD", at, `,"to_amount":"254000"`)); rec.Code != 201 || b["rate_source"] != "manual" || b["exchange_rate"] != "25400" {
		t.Errorf("manual VND: %d %v", rec.Code, b)
	}
	// Same currency with equal to_amount: fine, nothing extra stored.
	rec, b := do(t, h, "POST", "/api/transactions", transferBody(usd, usd2, "10", "USD", at, `,"to_amount":"10.00","to_currency":"USD"`))
	if rec.Code != 201 || b["to_amount"] != nil || b["exchange_rate"] != nil || b["to_currency"] != nil {
		t.Errorf("same currency: %d %v", rec.Code, b)
	}
	if balanceOf(t, h, usd) != "9980.00" || balanceOf(t, h, usd2) != "10010.00" || balanceOf(t, h, thb) != "10000.00" {
		t.Errorf("rejected requests moved balances: %s %s %s", balanceOf(t, h, usd), balanceOf(t, h, usd2), balanceOf(t, h, thb))
	}
}

func TestCrossCurrencyUpstreamDown(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close()
	h, _ := newFXServer(t, url)
	usd := seedPayable(t, h, "Dollars", "USD")
	thb := seedPayable(t, h, "Baht", "THB")
	rec, b := do(t, h, "POST", "/api/transactions", transferBody(usd, thb, "10", "USD", "2026-10-09T09:00:00Z", ""))
	if rec.Code != 503 || b["code"] != "exchange_rate_unavailable" || !strings.Contains(b["error"].(string), "to_amount") {
		t.Errorf("down: %d %v", rec.Code, b)
	}
	if rec, _ := do(t, h, "GET", "/api/exchange-rates?from=USD&to=THB", ""); rec.Code != 503 {
		t.Errorf("preview down: %d", rec.Code)
	}
	// Upstream 5xx -> 503; garbage -> 502.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("to") == "THB" {
			w.WriteHeader(500)
			return
		}
		_, _ = w.Write([]byte("<html>"))
	}))
	defer bad.Close()
	h2, _ := newFXServer(t, bad.URL)
	if rec, _ := do(t, h2, "GET", "/api/exchange-rates?from=USD&to=THB", ""); rec.Code != 503 {
		t.Errorf("5xx: %d", rec.Code)
	}
	if rec, _ := do(t, h2, "GET", "/api/exchange-rates?from=USD&to=JPY", ""); rec.Code != 502 {
		t.Errorf("garbage: %d", rec.Code)
	}
	// Manual still works while Frankfurter is down.
	if rec, b := do(t, h, "POST", "/api/transactions", transferBody(usd, thb, "10", "USD", "2026-10-09T09:00:00Z", `,"to_amount":"336"`)); rec.Code != 201 {
		t.Errorf("manual while down: %d %v", rec.Code, b)
	}
	if balanceOf(t, h, usd) != "9990.00" || balanceOf(t, h, thb) != "10336.00" {
		t.Errorf("balances %s %s", balanceOf(t, h, usd), balanceOf(t, h, thb))
	}
}
