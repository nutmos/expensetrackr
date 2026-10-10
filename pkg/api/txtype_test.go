package api

import (
	"fmt"
	"net/http"
	"testing"
)

func seedBal(t *testing.T, h http.Handler, name, typ string) string {
	t.Helper()
	body := fmt.Sprintf(`{"name":%q,"type":%q,"currency":"THB","balance":"0"}`, name, typ)
	if typ == "credit_card" || typ == "other_liability" {
		body = fmt.Sprintf(`{"name":%q,"type":%q,"currency":"THB","debt":"0","limit":"1000"}`, name, typ)
	}
	rec, b := do(t, h, http.MethodPost, "/api/balances", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("seed %s: %d %v", name, rec.Code, b)
	}
	return b["uid"].(string)
}

func TestTransactionTypes(t *testing.T) {
	h := newTestServer(t)
	pay := seedBal(t, h, "Bank", "payment_account")
	card := seedBal(t, h, "Card", "credit_card")
	gold := seedBal(t, h, "Gold", "other_asset")
	loan := seedBal(t, h, "Loan", "other_liability")
	tx := func(extra string) string {
		return `{"amount":"10","currency":"THB","spent_at":"2026-10-07T10:00:00+08:00"` + extra + `}`
	}

	// Omitted type defaults to expense.
	rec, e := do(t, h, "POST", "/api/transactions", tx(`,"balance_uid":"`+pay+`"`))
	if rec.Code != 201 || e["type"] != "expense" || e["to_balance_uid"] != nil || e["to_account"] != nil {
		t.Fatalf("default expense: %d %v", rec.Code, e)
	}
	// Income into other_asset ok; into credit card rejected.
	if rec, b := do(t, h, "POST", "/api/transactions", tx(`,"type":"income","balance_uid":"`+gold+`"`)); rec.Code != 201 || b["type"] != "income" {
		t.Errorf("income: %d %v", rec.Code, b)
	}
	cases := []struct{ name, body, field string }{
		{"income to card", tx(`,"type":"income","balance_uid":"` + card + `"`), "balance_uid"},
		{"expense from other_asset", tx(`,"type":"expense","balance_uid":"` + gold + `"`), "balance_uid"},
		{"bad type", tx(`,"type":"refund","balance_uid":"` + pay + `"`), "type"},
		{"to on expense", tx(`,"balance_uid":"` + pay + `","to_balance_uid":"` + card + `"`), "to_balance_uid"},
		{"to on income", tx(`,"type":"income","balance_uid":"` + pay + `","to_balance_uid":"` + card + `"`), "to_balance_uid"},
		{"transfer no dest", tx(`,"type":"transfer","balance_uid":"` + pay + `"`), "to_balance_uid"},
		{"transfer same", tx(`,"type":"transfer","balance_uid":"` + pay + `","to_balance_uid":"` + pay + `"`), "to_balance_uid"},
		{"transfer unknown dest", tx(`,"type":"transfer","balance_uid":"` + pay + `","to_balance_uid":"00000000-0000-4000-8000-000000000000"`), "to_balance_uid"},
	}
	for _, c := range cases {
		rec, b := do(t, h, "POST", "/api/transactions", c.body)
		f, _ := b["fields"].(map[string]any)
		if rec.Code != 422 || f[c.field] == nil {
			t.Errorf("%s: %d %v", c.name, rec.Code, b)
		}
	}
	// Transfer to credit card (card payment) and to a loan.
	rec, tr := do(t, h, "POST", "/api/transactions", tx(`,"type":"transfer","balance_uid":"`+pay+`","to_balance_uid":"`+card+`"`))
	if rec.Code != 201 || tr["to_balance_uid"] != card || tr["to_account"] != "Card" || tr["account"] != "Bank" {
		t.Fatalf("transfer: %d %v", rec.Code, tr)
	}
	if rec, b := do(t, h, "POST", "/api/transactions", tx(`,"type":"transfer","balance_uid":"`+pay+`","to_balance_uid":"`+loan+`"`)); rec.Code != 201 {
		t.Errorf("transfer to loan: %d %v", rec.Code, b)
	}

	// List filter.
	_, l := do(t, h, "GET", "/api/transactions?type=transfer", "")
	if l["count"].(float64) != 2 {
		t.Errorf("type=transfer count %v", l["count"])
	}
	if rec, _ := do(t, h, "GET", "/api/transactions?type=nope", ""); rec.Code != 400 {
		t.Errorf("bad type filter: %d", rec.Code)
	}

	// PUT transfer -> expense: omit to_balance_uid.
	id := tr["uid"].(string)
	rec, p := putMerged(t, h, "/api/transactions/"+id, `{"type":"expense","to_balance_uid":null}`)
	if rec.Code != 200 || p["type"] != "expense" || p["to_balance_uid"] != nil || p["to_account"] != nil {
		t.Errorf("put to expense: %d %v", rec.Code, p)
	}
	// PUT expense -> transfer needs a destination.
	if rec, _ := putMerged(t, h, "/api/transactions/"+id, `{"type":"transfer","to_balance_uid":null}`); rec.Code != 422 {
		t.Errorf("put to transfer w/o dest: %d", rec.Code)
	}
	rec, p = putMerged(t, h, "/api/transactions/"+id, `{"type":"transfer","to_balance_uid":"`+gold+`"}`)
	if rec.Code != 200 || p["to_account"] != "Gold" {
		t.Errorf("put to transfer: %d %v", rec.Code, p)
	}
	// PUT of the full record with only the note changed keeps the transfer.
	rec, p = putMerged(t, h, "/api/transactions/"+id, `{"note":"x"}`)
	if rec.Code != 200 || p["to_balance_uid"] != gold {
		t.Errorf("patch note on transfer: %d %v", rec.Code, p)
	}
}
