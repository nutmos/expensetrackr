package api

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// thbSGD is the conversion_rates of 1000.00 THB -> 39.70 SGD.
var thbSGD = []any{
	map[string]any{"from": "THB", "to": "SGD", "rate": "0.0397"},
	map[string]any{"from": "SGD", "to": "THB", "rate": "25.18891688"},
}

func checkRates(t *testing.T, where string, tx map[string]any, want any) {
	t.Helper()
	got, ok := tx["conversion_rates"]
	if !ok {
		t.Errorf("%s: conversion_rates missing in %v", where, tx)
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s: conversion_rates = %v, want %v", where, got, want)
	}
}

// TestTransferConversionRates checks that conversion_rates is computed from
// the transaction's own amounts on every response that returns a transaction.
func TestTransferConversionRates(t *testing.T) {
	fake, _ := fakeFrankfurter(t)
	h, _ := newFXServer(t, fake.URL)
	thb := seedPayable(t, h, "Baht", "THB")
	sgd := seedPayable(t, h, "Dollars", "SGD")
	thb2 := seedPayable(t, h, "Baht 2", "THB")
	usd := seedPayable(t, h, "US", "USD")
	at := "2026-10-09T09:00:00+08:00"

	// Create (manual rate).
	path, b := create(t, h, transferBody(thb, sgd, "1000", "THB", at, `,"to_amount":"39.70"`))
	checkRates(t, "create", b, thbSGD)
	if b["exchange_rate"] != "0.0397" {
		t.Errorf("exchange_rate %v", b["exchange_rate"])
	}

	// GET single and list.
	rec, b := do(t, h, "GET", path, "")
	if rec.Code != 200 {
		t.Fatalf("get: %d", rec.Code)
	}
	checkRates(t, "get", b, thbSGD)
	_, lb := do(t, h, "GET", "/api/transactions?type=transfer", "")
	items := lb["transactions"].([]any)
	if len(items) != 1 {
		t.Fatalf("list: %v", lb)
	}
	checkRates(t, "list", items[0].(map[string]any), thbSGD)

	// Update: rates follow the new amounts.
	rec, b = do(t, h, "PUT", path, transferBody(thb, sgd, "500", "THB", at, `,"to_amount":"20"`))
	if rec.Code != 200 {
		t.Fatalf("put: %d %v", rec.Code, b)
	}
	checkRates(t, "put", b, []any{
		map[string]any{"from": "THB", "to": "SGD", "rate": "0.04"},
		map[string]any{"from": "SGD", "to": "THB", "rate": "25"},
	})

	// 409 conflict body carries the current transaction with its rates.
	rec, b = doH(t, h, "PUT", path, transferBody(thb, sgd, "1", "THB", at, `,"to_amount":"1"`), map[string]string{"If-Match": `"1"`})
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale put: %d %v", rec.Code, b)
	}
	checkRates(t, "409 current", b["current"].(map[string]any), []any{
		map[string]any{"from": "THB", "to": "SGD", "rate": "0.04"},
		map[string]any{"from": "SGD", "to": "THB", "rate": "25"},
	})

	// Read-only: sending it back is rejected like any other read-only field
	// (400 unknown field) and nothing changes.
	rec, b = do(t, h, "PUT", path, transferBody(thb, sgd, "500", "THB", at,
		`,"to_amount":"20","conversion_rates":[{"from":"THB","to":"SGD","rate":"9"}]`))
	if rec.Code != http.StatusBadRequest || !strings.Contains(b["error"].(string), "conversion_rates") {
		t.Errorf("put with conversion_rates: %d %v", rec.Code, b)
	}
	if rec, _ := do(t, h, "POST", "/api/transactions", transferBody(thb, sgd, "1", "THB", at, `,"conversion_rates":null`)); rec.Code != http.StatusBadRequest {
		t.Errorf("post with conversion_rates: %d", rec.Code)
	}
	if _, b := do(t, h, "GET", path, ""); b["version"].(float64) != 2 || b["to_amount"] != "20.00" {
		t.Errorf("changed by rejected put: %v", b)
	}
	// The web form's PUT (explicit fields, no read-only ones) still works.
	rec, b = do(t, h, "PUT", path, `{"type":"transfer","amount":"500","currency":"THB","balance_uid":"`+thb+
		`","to_balance_uid":"`+sgd+`","spent_at":"`+at+`","note":"edited in UI","to_amount":"20"}`)
	if rec.Code != 200 || b["note"] != "edited in UI" {
		t.Fatalf("web-style put: %d %v", rec.Code, b)
	}
	checkRates(t, "web put", b, []any{
		map[string]any{"from": "THB", "to": "SGD", "rate": "0.04"},
		map[string]any{"from": "SGD", "to": "THB", "rate": "25"},
	})

	// Frankfurter rate: derived from the stored (rounded) amounts.
	_, b = create(t, h, transferBody(usd, thb, "100", "USD", "2026-10-10T09:00:00+07:00", ""))
	checkRates(t, "frankfurter", b, []any{
		map[string]any{"from": "USD", "to": "THB", "rate": "33.615"},
		map[string]any{"from": "THB", "to": "USD", "rate": "0.02974862413"},
	})

	// Same-currency transfer and non-transfer types: null.
	_, b = create(t, h, transferBody(thb, thb2, "100", "THB", at, ""))
	checkRates(t, "same currency", b, nil)
	_, b = create(t, h, `{"type":"expense","amount":"5","currency":"THB","balance_uid":"`+thb+`","spent_at":"`+at+`"}`)
	checkRates(t, "expense", b, nil)
	_, lb = do(t, h, "GET", "/api/transactions", "")
	for _, it := range lb["transactions"].([]any) {
		m := it.(map[string]any)
		if _, ok := m["conversion_rates"]; !ok {
			t.Errorf("list item without conversion_rates: %v", m)
		}
		if (m["to_currency"] == nil) != (m["conversion_rates"] == nil) {
			t.Errorf("conversion_rates should be set exactly for cross-currency transfers: %v", m)
		}
	}
}

func TestBulkConversionRates(t *testing.T) {
	fake, _ := fakeFrankfurter(t)
	h, _ := newFXServer(t, fake.URL)
	thb := seedPayable(t, h, "Baht", "THB")
	sgd := seedPayable(t, h, "Dollars", "SGD")
	thb2 := seedPayable(t, h, "Baht 2", "THB")
	at := "2026-10-09T09:00:00+08:00"
	items := []string{
		transferBody(thb, sgd, "1000", "THB", at, `,"to_amount":"39.70"`),
		transferBody(thb, thb2, "10", "THB", at, ""),
	}
	for _, opts := range []string{`"dry_run":true`, ""} {
		rec, b := do(t, h, "POST", "/api/transactions/bulk", bulkBody(opts, items...))
		if rec.Code != 200 && rec.Code != 201 {
			t.Fatalf("bulk %s: %d %s", opts, rec.Code, rec.Body)
		}
		rs := results(t, b)
		checkRates(t, "bulk "+opts, rs[0]["transaction"].(map[string]any), thbSGD)
		checkRates(t, "bulk same currency "+opts, rs[1]["transaction"].(map[string]any), nil)
	}
	// A bulk item carrying conversion_rates is rejected like other unknown fields.
	rec, b := do(t, h, "POST", "/api/transactions/bulk", bulkBody("",
		transferBody(thb, sgd, "1", "THB", at, `,"to_amount":"1","conversion_rates":[]`)))
	if rec.Code == 200 || rec.Code == 201 {
		t.Errorf("bulk with conversion_rates accepted: %d %v", rec.Code, b)
	}
}
