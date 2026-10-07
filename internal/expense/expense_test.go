package expense

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestParseTimestamp(t *testing.T) {
	ok := []string{
		"2026-10-06T21:06:00+08:00",
		"2026-10-06T13:06:00Z",
		"2026-10-06T21:06:00.123+08:00",
		"2026-10-06T08:06:00-05:00",
	}
	for _, s := range ok {
		if _, err := ParseTimestamp(s); err != nil {
			t.Errorf("ParseTimestamp(%q) unexpected error: %v", s, err)
		}
	}
	bad := []string{
		"", "2026-10-06", "2026-10-06T21:06:00", "2026-10-06 21:06:00+08:00",
		"2026-10-06T21:06+08:00", "06/10/2026 21:06", "2026-13-01T00:00:00Z", "now",
	}
	for _, s := range bad {
		if _, err := ParseTimestamp(s); err == nil {
			t.Errorf("ParseTimestamp(%q) expected error", s)
		}
	}
}

func TestValidateNormalizes(t *testing.T) {
	in := CreateInput{
		Amount:   "120.5",
		Currency: " thb ",
		Account:  "  KBank debit ",
		SpentAt:  "2026-10-06T21:06:00.999+08:00",
		Note:     " lunch ",
	}
	e, err := in.Validate()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if e.Currency != "THB" || e.Amount != "120.50" || e.AmountMinor != 12050 {
		t.Errorf("bad amount/currency normalization: %+v", e)
	}
	if e.Account != "KBank debit" || e.Note != "lunch" {
		t.Errorf("bad trimming: %+v", e)
	}
	if e.SpentAt != "2026-10-06T21:06:00+08:00" {
		t.Errorf("SpentAt = %q, want offset preserved at second precision", e.SpentAt)
	}
}

func TestValidateReportsAllFields(t *testing.T) {
	_, err := CreateInput{Amount: "abc", Currency: "XYZ", SpentAt: "2026-10-06T21:06:00"}.Validate()
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %v", err)
	}
	for _, f := range []string{"currency", "account", "spent_at"} {
		if _, ok := ve.Fields[f]; !ok {
			t.Errorf("expected error for field %q, got %v", f, ve.Fields)
		}
	}

	_, err = CreateInput{Amount: "1.5", Currency: "JPY", Account: "Cash", SpentAt: "2026-10-06T21:06:00+09:00"}.Validate()
	if !errors.As(err, &ve) || ve.Fields["amount"] == "" {
		t.Errorf("expected amount error for fractional JPY, got %v", err)
	}
}

func TestDecimalInputAcceptsStringOrNumber(t *testing.T) {
	var in CreateInput
	if err := json.Unmarshal([]byte(`{"amount": 0.1}`), &in); err != nil || in.Amount != "0.1" {
		t.Errorf("number literal: got %q, err %v", in.Amount, err)
	}
	if err := json.Unmarshal([]byte(`{"amount": "19.99"}`), &in); err != nil || in.Amount != "19.99" {
		t.Errorf("string: got %q, err %v", in.Amount, err)
	}
}
