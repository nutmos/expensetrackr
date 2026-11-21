package api

import (
	"net/http"
	"net/url"
	"testing"
	"testing/fstest"

	"github.com/gin-gonic/gin"
	"github.com/nutmos/expensetrackr/pkg/store/storetest"
)

func mustTx(t *testing.T, h http.Handler, body string) {
	t.Helper()
	if rec, b := do(t, h, "POST", "/api/transactions", body); rec.Code != http.StatusCreated {
		t.Fatalf("create tx %s: %d %v", body, rec.Code, b)
	}
}

func TestReportCatalog(t *testing.T) {
	h := newTestServer(t)
	rec, body := do(t, h, "GET", "/api/reports", "")
	if rec.Code != 200 || body["count"].(float64) != 2 {
		t.Fatalf("catalog: %d %v", rec.Code, body)
	}
	first := body["reports"].([]any)[0].(map[string]any)
	if first["id"] != "income-expense" || first["path"] != "/api/reports/income-expense" {
		t.Errorf("first report: %v", first)
	}
	// The web page routes exist for known reports only.
	if rec, _ := do(t, h, "GET", "/reports", ""); rec.Code != 200 {
		t.Errorf("/reports page: %d", rec.Code)
	}
	if rec, _ := do(t, h, "GET", "/reports/assets-liabilities", ""); rec.Code != 200 {
		t.Errorf("/reports/assets-liabilities page: %d", rec.Code)
	}
	if rec, _ := do(t, h, "GET", "/reports/nope", ""); rec.Code != 404 {
		t.Errorf("/reports/nope page: %d", rec.Code)
	}
}

func TestIncomeExpenseReport(t *testing.T) {
	h := newTestServer(t)
	cash := seedPayable(t, h, "Cash", "THB")
	card := seedCard(t, h, "Card", "THB")
	usd := seedPayable(t, h, "USD acct", "USD")
	gold := mkBal(t, h, `{"name":"Gold","type":"other_asset","currency":"THB","balance":"0"}`)
	food := mkCat(t, h, "Food", "expense")["uid"].(string)
	travel := mkCat(t, h, "Travel", "expense")["uid"].(string)
	salary := mkCat(t, h, "Salary", "income")["uid"].(string)
	divs := mkCat(t, h, "Dividends", "income")["uid"].(string)

	tx := func(typ, amount, bal, cat, at string, extra string) {
		mustTx(t, h, `{"type":"`+typ+`","amount":"`+amount+`","currency":"`+map[string]string{cash: "THB", card: "THB", usd: "USD"}[bal]+
			`","balance_uid":"`+bal+`","category_uid":"`+cat+`","spent_at":"`+at+`"`+extra+`}`)
	}
	tx("expense", "100.25", cash, food, "2026-11-02T12:00:00+07:00", "")
	tx("expense", "50", card, food, "2026-11-03T12:00:00+07:00", "")
	tx("expense", "300", cash, travel, "2026-11-04T12:00:00+07:00", "")
	tx("income", "1000", cash, salary, "2026-11-01T09:00:00+07:00", "")
	tx("return_on_investment", "20", cash, divs, "2026-11-05T09:00:00+07:00", `,"to_balance_uid":"`+gold+`"`)
	tx("expense", "7.5", usd, food, "2026-11-06T09:00:00+07:00", "")
	tx("expense", "999", cash, food, "2026-10-31T23:59:59+07:00", "")                                     // before the range
	tx("asset_purchase", "40", cash, travel, "2026-11-07T09:00:00+07:00", `,"to_balance_uid":"`+gold+`"`) // not counted
	mustTx(t, h, `{"type":"repay_debt","amount":"10","currency":"THB","balance_uid":"`+cash+`","to_balance_uid":"`+card+`","spent_at":"2026-11-07T10:00:00+07:00"}`)
	// A manual balance edit records a balance_adjustment: not counted either.
	if rec, b := do(t, h, "PUT", "/api/balances/"+cash, `{"name":"Cash","type":"payment_account","currency":"THB","balance":"1"}`); rec.Code != 200 {
		t.Fatalf("manual edit: %d %v", rec.Code, b)
	}

	q := url.Values{"from": {"2026-11-01T00:00:00+07:00"}, "to": {"2026-11-30T23:59:59+07:00"}}
	rec, body := do(t, h, "GET", "/api/reports/income-expense?"+q.Encode(), "")
	if rec.Code != 200 {
		t.Fatalf("report: %d %v", rec.Code, body)
	}
	if body["report"] != "income-expense" || body["from"] != "2026-11-01T00:00:00+07:00" || body["to"] != "2026-11-30T23:59:59+07:00" {
		t.Errorf("header: %v", body)
	}
	curs := body["currencies"].([]any)
	if len(curs) != 2 {
		t.Fatalf("currencies: %v", curs)
	}
	thb, us := curs[0].(map[string]any), curs[1].(map[string]any)
	if thb["currency"] != "THB" || thb["expense"] != "450.25" || thb["income"] != "1020.00" || thb["net"] != "569.75" ||
		thb["expense_count"].(float64) != 3 || thb["income_count"].(float64) != 2 {
		t.Errorf("THB: %v", thb)
	}
	exp := thb["expense_by_category"].([]any)
	if len(exp) != 2 {
		t.Fatalf("THB expense categories: %v", exp)
	}
	top := exp[0].(map[string]any)
	if top["category_name"] != "Travel" || top["amount"] != "300.00" || top["amount_minor"] != "30000" || top["count"].(float64) != 1 {
		t.Errorf("top expense category: %v", top)
	}
	if f := exp[1].(map[string]any); f["category_name"] != "Food" || f["amount"] != "150.25" || f["count"].(float64) != 2 {
		t.Errorf("food: %v", f)
	}
	inc := thb["income_by_category"].([]any)
	if len(inc) != 2 || inc[0].(map[string]any)["category_name"] != "Salary" || inc[1].(map[string]any)["category_name"] != "Dividends" {
		t.Errorf("THB income categories: %v", inc)
	}
	if us["currency"] != "USD" || us["expense"] != "7.50" || us["income"] != "0.00" || us["net"] != "-7.50" ||
		len(us["income_by_category"].([]any)) != 0 {
		t.Errorf("USD: %v", us)
	}

	// No range: everything, including October.
	_, body = do(t, h, "GET", "/api/reports/income-expense", "")
	if thb := body["currencies"].([]any)[0].(map[string]any); thb["expense"] != "1449.25" || body["from"] != nil || body["to"] != nil {
		t.Errorf("all time: %v", body)
	}
	// A '+' left unencoded in the query decodes to a space; still accepted.
	rec, body = do(t, h, "GET", "/api/reports/income-expense?from=2026-11-06T00:00:00+07:00", "")
	if rec.Code != 200 || len(body["currencies"].([]any)) != 1 || body["from"] != "2026-11-06T00:00:00+07:00" { // only the USD expense
		t.Errorf("unencoded plus: %d %v", rec.Code, body)
	}

	for _, bad := range []string{"from=yesterday", "to=2026-11-01", "from=2026-11-02T00:00:00Z&to=2026-11-01T00:00:00Z"} {
		if rec, b := do(t, h, "GET", "/api/reports/income-expense?"+bad, ""); rec.Code != 400 || fieldsOf(b) == nil {
			t.Errorf("%s: %d %v", bad, rec.Code, b)
		}
	}
}

// mkBal creates a balance and returns its uid.
func mkBal(t *testing.T, h http.Handler, body string) string {
	t.Helper()
	_, b := createBal(t, h, body)
	return b["uid"].(string)
}

func TestAssetsLiabilitiesReport(t *testing.T) {
	h := newTestServer(t)
	_, body := do(t, h, "GET", "/api/reports/assets-liabilities", "")
	if cs := body["currencies"].([]any); len(cs) != 0 || body["as_of"] == nil {
		t.Errorf("empty report: %v", body)
	}

	mkBal(t, h, `{"name":"Wallet","type":"payment_account","currency":"THB","balance":"500"}`)
	mkBal(t, h, `{"name":"Overdrawn","type":"payment_account","currency":"THB","balance":"-20"}`)
	mkBal(t, h, `{"name":"Gold","type":"other_asset","currency":"THB","balance":"10000"}`)
	mkBal(t, h, `{"name":"Visa","type":"credit_card","currency":"THB","debt":"1500","limit":"50000"}`)
	mkBal(t, h, `{"name":"Car loan","type":"other_liability","currency":"THB","debt":"200000","limit":"300000"}`)
	mkBal(t, h, `{"name":"Yen","type":"payment_account","currency":"JPY","balance":"3000"}`)

	rec, body := do(t, h, "GET", "/api/reports/assets-liabilities", "")
	if rec.Code != 200 || body["report"] != "assets-liabilities" {
		t.Fatalf("report: %d %v", rec.Code, body)
	}
	curs := body["currencies"].([]any)
	if len(curs) != 2 {
		t.Fatalf("currencies: %v", curs)
	}
	jpy, thb := curs[0].(map[string]any), curs[1].(map[string]any)
	if jpy["currency"] != "JPY" || jpy["assets"] != "3000" || jpy["liabilities"] != "0" || jpy["net"] != "3000" {
		t.Errorf("JPY: %v", jpy)
	}
	if thb["assets"] != "10480.00" || thb["liabilities"] != "201500.00" || thb["net"] != "-191020.00" {
		t.Errorf("THB totals: %v", thb)
	}
	var names []string
	for _, a := range thb["accounts"].([]any) {
		names = append(names, a.(map[string]any)["name"].(string))
	}
	if got, want := join(names), "Gold,Wallet,Overdrawn,Car loan,Visa"; got != want {
		t.Errorf("account order %s, want %s", got, want)
	}
	types := thb["by_type"].([]any)
	if len(types) != 4 {
		t.Fatalf("by_type: %v", types)
	}
	if pa := types[0].(map[string]any); pa["type"] != "payment_account" || pa["amount"] != "480.00" || pa["count"].(float64) != 2 || pa["kind"] != "asset" {
		t.Errorf("payment_account: %v", pa)
	}
	if cc := types[1].(map[string]any); cc["type"] != "credit_card" || cc["amount"] != "1500.00" || cc["kind"] != "liability" {
		t.Errorf("credit_card: %v", cc)
	}
}

// Reports only see the caller's own data.
func TestReportsArePerUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	st := storetest.Open(t)
	router := (&Server{Store: st, Static: fstest.MapFS{"index.html": {Data: []byte("x")}}}).Router()
	alice := asUser(t, router, st, "alice")
	bob := asUser(t, router, st, "bob")

	cash := seedPayable(t, alice, "Cash", "THB")
	mustTx(t, alice, `{"amount":"10","currency":"THB","balance_uid":"`+cash+`","spent_at":"2026-10-01T10:00:00+07:00"}`)

	for _, path := range []string{"/api/reports/income-expense", "/api/reports/assets-liabilities"} {
		rec, b := do(t, bob, "GET", path, "")
		if rec.Code != 200 || len(b["currencies"].([]any)) != 0 {
			t.Errorf("bob %s: %d %v", path, rec.Code, b)
		}
		if _, b := do(t, alice, "GET", path, ""); len(b["currencies"].([]any)) != 1 {
			t.Errorf("alice %s: %v", path, b)
		}
	}
}
