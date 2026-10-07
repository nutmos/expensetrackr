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
	if _, has := food["id"]; has || !uuidV4RE.MatchString(food["uid"].(string)) || food["updated_at"] != nil {
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

	// PUT / PATCH keep uid; PATCH description null clears; rename to dup -> 409.
	rec, b = do(t, h, "PUT", path, `{"name":"Meals","type":"expense","description":"eat","uid":"11111111-1111-4111-8111-111111111111"}`)
	if rec.Code != 200 || b["uid"] != uid || b["name"] != "Meals" || b["updated_at"] == nil {
		t.Errorf("PUT: %d %v", rec.Code, b)
	}
	rec, b = do(t, h, "PATCH", path, `{"description":null}`)
	if rec.Code != 200 || b["description"] != "" || b["name"] != "Meals" {
		t.Errorf("PATCH: %d %v", rec.Code, b)
	}
	mkCat(t, h, "Transport", "expense")
	if rec, _ := do(t, h, "PATCH", path, `{"name":"transport"}`); rec.Code != 409 {
		t.Errorf("rename dup: %d", rec.Code)
	}
	// Unreferenced type change is allowed.
	if rec, b := do(t, h, "PATCH", path, `{"type":"income"}`); rec.Code != 200 || b["type"] != "income" {
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
	if rec.Code != 201 || e["category_uid"] != food || e["category"] != "Food" {
		t.Fatalf("expense with category: %d %v", rec.Code, e)
	}
	// No category is fine (null).
	rec, n := do(t, h, "POST", "/api/transactions", tx(``))
	if rec.Code != 201 || n["category_uid"] != nil || n["category"] != nil {
		t.Errorf("no category: %d %v", rec.Code, n)
	}
	if rec, b := do(t, h, "POST", "/api/transactions", tx(`,"type":"income","category_uid":"`+salary+`"`)); rec.Code != 201 || b["category"] != "Salary" {
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
	// (transaction keeps its name snapshot).
	if rec, _ := do(t, h, "DELETE", "/api/categories/"+food, ""); rec.Code != 409 {
		t.Errorf("delete referenced: %d", rec.Code)
	}
	if rec, _ := do(t, h, "PATCH", "/api/categories/"+food, `{"type":"income"}`); rec.Code != 409 {
		t.Errorf("type change referenced: %d", rec.Code)
	}
	if rec, _ := do(t, h, "PATCH", "/api/categories/"+food, `{"name":"Meals"}`); rec.Code != 200 {
		t.Errorf("rename referenced: %d", rec.Code)
	}
	txPath := "/api/transactions/" + e["uid"].(string)
	if _, g := do(t, h, "GET", txPath, ""); g["category"] != "Food" {
		t.Errorf("snapshot changed: %v", g["category"])
	}

	// PATCH: type change drops category; clear with null; then delete works.
	rec, p := do(t, h, "PATCH", txPath, `{"type":"income"}`)
	if rec.Code != 200 || p["category_uid"] != nil || p["category"] != nil {
		t.Errorf("type change drops category: %d %v", rec.Code, p)
	}
	rec, p = do(t, h, "PATCH", txPath, `{"type":"expense","category_uid":"`+food+`"}`)
	if rec.Code != 200 || p["category"] != "Meals" {
		t.Errorf("set category: %d %v", rec.Code, p)
	}
	rec, p = do(t, h, "PATCH", txPath, `{"category_uid":null}`)
	if rec.Code != 200 || p["category_uid"] != nil {
		t.Errorf("clear category: %d %v", rec.Code, p)
	}
	if rec, _ := do(t, h, "DELETE", "/api/categories/"+food, ""); rec.Code != 204 {
		t.Errorf("delete after unreferenced: %d", rec.Code)
	}
}
