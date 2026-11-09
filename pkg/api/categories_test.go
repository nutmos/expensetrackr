package api

import (
	"net/http"
	"strings"
	"testing"
)

func mkCat(t *testing.T, h http.Handler, name, typ string) map[string]any {
	t.Helper()
	rec, b := do(t, h, "POST", "/api/categories", `{"name":"`+name+`","type":"`+typ+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create category %s: %d %v", name, rec.Code, b)
	}
	if rec.Header().Get("Location") != "/api/categories/"+b["uid"].(string) {
		t.Errorf("Location = %q", rec.Header().Get("Location"))
	}
	return b
}

func TestCategoriesCRUD(t *testing.T) {
	h := newTestServer(t)
	food := mkCat(t, h, "Food", "expense")
	if _, has := food["id"]; has || !isUUIDv4(food["uid"].(string)) || food["updated_at"] != nil {
		t.Fatalf("create body: %v", food)
	}
	mkCat(t, h, "Salary", "income")
	mkCat(t, h, "food", "income") // same name, other type: allowed

	// Duplicate within a type (case-insensitive) -> 409 on name.
	rec, b := do(t, h, "POST", "/api/categories", `{"name":"FOOD","type":"expense"}`)
	if f, _ := b["fields"].(map[string]any); rec.Code != 409 || f["name"] == nil {
		t.Errorf("duplicate: %d %v", rec.Code, b)
	}
	// Validation.
	rec, b = do(t, h, "POST", "/api/categories", `{"name":"","type":"refund"}`)
	if f, _ := b["fields"].(map[string]any); rec.Code != 422 || f["name"] == nil || f["type"] == nil {
		t.Errorf("validation: %d %v", rec.Code, b)
	}
	if rec, _ := do(t, h, "POST", "/api/categories", `{"name":"x","type":"expense","colour":"red"}`); rec.Code != 400 {
		t.Errorf("unknown field: %d", rec.Code)
	}

	// List + filter.
	_, l := do(t, h, "GET", "/api/categories", "")
	if l["count"].(float64) != 3 {
		t.Errorf("list count %v", l["count"])
	}
	_, l = do(t, h, "GET", "/api/categories?type=income", "")
	if l["count"].(float64) != 2 {
		t.Errorf("income count %v", l["count"])
	}
	if rec, _ := do(t, h, "GET", "/api/categories?type=x", ""); rec.Code != 400 {
		t.Errorf("bad filter: %d", rec.Code)
	}

	uid := food["uid"].(string)
	path := "/api/categories/" + uid
	// Paths: malformed 400, unknown 404, upper-case ok.
	if rec, _ := do(t, h, "GET", "/api/categories/1", ""); rec.Code != 400 {
		t.Errorf("numeric: %d", rec.Code)
	}
	if rec, _ := do(t, h, "GET", "/api/categories/00000000-0000-4000-8000-000000000000", ""); rec.Code != 404 {
		t.Errorf("unknown: %d", rec.Code)
	}
	if rec, _ := do(t, h, "GET", "/api/categories/"+strings.ToUpper(uid), ""); rec.Code != 200 {
		t.Errorf("upper: %d", rec.Code)
	}

	// PUT keeps uid; omitting description clears it; rename to dup -> 409.
	rec, b = do(t, h, "PUT", path, `{"name":"Meals","type":"expense","description":"eat","uid":"11111111-1111-4111-8111-111111111111"}`)
	if rec.Code != 200 || b["uid"] != uid || b["name"] != "Meals" || b["updated_at"] == nil {
		t.Errorf("PUT: %d %v", rec.Code, b)
	}
	rec, b = putMerged(t, h, path, `{"description":null}`)
	if rec.Code != 200 || b["description"] != "" || b["name"] != "Meals" {
		t.Errorf("PUT: %d %v", rec.Code, b)
	}
	mkCat(t, h, "Transport", "expense")
	if rec, _ := putMerged(t, h, path, `{"name":"transport"}`); rec.Code != 409 {
		t.Errorf("rename dup: %d", rec.Code)
	}
	// Unreferenced type change is allowed.
	if rec, b := putMerged(t, h, path, `{"type":"income"}`); rec.Code != 200 || b["type"] != "income" {
		t.Errorf("type change: %d %v", rec.Code, b)
	}
	if rec, _ := do(t, h, "DELETE", path, ""); rec.Code != 204 {
		t.Errorf("delete: %d", rec.Code)
	}
	if rec, _ := do(t, h, "DELETE", path, ""); rec.Code != 404 {
		t.Errorf("delete again: %d", rec.Code)
	}
}

func TestTransactionCategories(t *testing.T) {
	h := newTestServer(t)
	bank := seedBal(t, h, "Bank", "payment_account")
	card := seedBal(t, h, "Card", "credit_card")
	food := mkCat(t, h, "Food", "expense")["uid"].(string)
	salary := mkCat(t, h, "Salary", "income")["uid"].(string)
	tx := func(extra string) string {
		return `{"amount":"10","currency":"THB","balance_uid":"` + bank + `","spent_at":"2026-10-07T10:00:00+08:00"` + extra + `}`
	}

	rec, e := do(t, h, "POST", "/api/transactions", tx(`,"category_uid":"`+strings.ToUpper(food)+`"`))
	if _, has := e["category"]; rec.Code != 201 || e["category_uid"] != food || has {
		t.Fatalf("expense with category: %d %v", rec.Code, e)
	}
	// A category is required for every type but transfer: missing, null or
	// "" is a 422 on category_uid (doH: sent as is, no default category).
	for _, c := range []struct{ name, body, msg string }{
		{"expense, missing", tx(``), "is required for an expense"},
		{"expense, null", tx(`,"category_uid":null`), "is required for an expense"},
		{"expense, empty", tx(`,"category_uid":"  "`), "is required for an expense"},
		{"income", tx(`,"type":"income"`), "is required for an income"},
		{"asset purchase", tx(`,"type":"asset_purchase","to_balance_uid":"` + card + `"`), "is required for an asset purchase"},
		{"return on investment", tx(`,"type":"return_on_investment","to_balance_uid":"` + card + `"`), "is required for a return on investment"},
	} {
		rec, b := doH(t, h, "POST", "/api/transactions", c.body, nil)
		if f, _ := b["fields"].(map[string]any); rec.Code != 422 || f["category_uid"] != c.msg {
			t.Errorf("no category, %s: %d %v", c.name, rec.Code, b)
		}
	}
	// A transfer needs none.
	if rec, b := doH(t, h, "POST", "/api/transactions", tx(`,"type":"transfer","to_balance_uid":"`+card+`"`), nil); rec.Code != 201 || b["category_uid"] != nil {
		t.Errorf("transfer without category: %d %v", rec.Code, b)
	}
	if rec, b := do(t, h, "POST", "/api/transactions", tx(`,"type":"income","category_uid":"`+salary+`"`)); rec.Code != 201 || b["category_uid"] != salary {
		t.Errorf("income with category: %d %v", rec.Code, b)
	}
	cases := []struct{ name, body string }{
		{"expense with income category", tx(`,"category_uid":"` + salary + `"`)},
		{"income with expense category", tx(`,"type":"income","category_uid":"` + food + `"`)},
		{"transfer with category", tx(`,"type":"transfer","to_balance_uid":"` + card + `","category_uid":"` + food + `"`)},
		{"unknown category", tx(`,"category_uid":"00000000-0000-4000-8000-000000000000"`)},
		{"malformed category", tx(`,"category_uid":"food"`)},
	}
	for _, c := range cases {
		rec, b := do(t, h, "POST", "/api/transactions", c.body)
		if f, _ := b["fields"].(map[string]any); rec.Code != 422 || f["category_uid"] == nil {
			t.Errorf("%s: %d %v", c.name, rec.Code, b)
		}
	}

	// Filter by category.
	_, l := do(t, h, "GET", "/api/transactions?category_uid="+food, "")
	if l["count"].(float64) != 1 {
		t.Errorf("category filter: %v", l["count"])
	}
	if rec, _ := do(t, h, "GET", "/api/transactions?category_uid=nope", ""); rec.Code != 400 {
		t.Errorf("bad category filter: %d", rec.Code)
	}

	// Referenced category: delete and type change rejected with 409; rename ok
	// (the transaction only holds the uid, so it follows the category).
	if rec, _ := do(t, h, "DELETE", "/api/categories/"+food, ""); rec.Code != 409 {
		t.Errorf("delete referenced: %d", rec.Code)
	}
	if rec, _ := putMerged(t, h, "/api/categories/"+food, `{"type":"income"}`); rec.Code != 409 {
		t.Errorf("type change referenced: %d", rec.Code)
	}
	if rec, _ := putMerged(t, h, "/api/categories/"+food, `{"name":"Meals"}`); rec.Code != 200 {
		t.Errorf("rename referenced: %d", rec.Code)
	}
	txPath := "/api/transactions/" + e["uid"].(string)
	if _, g := do(t, h, "GET", txPath, ""); g["category_uid"] != food {
		t.Errorf("category_uid changed: %v", g)
	}

	// PUT full record: a type change must also change the category to one of
	// the new type; the category can't be cleared; once no transaction uses
	// it, the category can be deleted.
	rec, p := putMerged(t, h, txPath, `{"type":"income","category_uid":"`+salary+`"}`)
	if rec.Code != 200 || p["category_uid"] != salary {
		t.Errorf("type change with new category: %d %v", rec.Code, p)
	}
	rec, p = putMerged(t, h, txPath, `{"type":"expense","category_uid":"`+food+`"}`)
	if rec.Code != 200 || p["category_uid"] != food {
		t.Errorf("set category: %d %v", rec.Code, p)
	}
	rec, p = putMerged(t, h, txPath, `{"category_uid":""}`)
	if f, _ := p["fields"].(map[string]any); rec.Code != 422 || f["category_uid"] != "is required for an expense" {
		t.Errorf("clear category: %d %v", rec.Code, p)
	}
	other := mkCat(t, h, "Other", "expense")["uid"].(string)
	if rec, p := putMerged(t, h, txPath, `{"category_uid":"`+other+`"}`); rec.Code != 200 || p["category_uid"] != other {
		t.Errorf("move to another category: %d %v", rec.Code, p)
	}
	if rec, _ := do(t, h, "DELETE", "/api/categories/"+food, ""); rec.Code != 204 {
		t.Errorf("delete after unreferenced: %d", rec.Code)
	}
}
