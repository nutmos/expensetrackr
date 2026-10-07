package expense

import (
	"encoding/json"
	"errors"
	"testing"

	"expense-service/internal/balance"
	"expense-service/internal/money"
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
		Amount:     "120.5",
		Currency:   " thb ",
		BalanceUID: "  A1B2C3D4-E5F6-4789-A012-3456789ABCDE ",
		SpentAt:    "2026-10-06T21:06:00.999+08:00",
		Note:       " lunch ",
	}
	e, err := in.Validate()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if e.Currency != "THB" || e.Amount != "120.50" || e.AmountMinor != 12050 {
		t.Errorf("bad amount/currency normalization: %+v", e)
	}
	if e.BalanceUID != "a1b2c3d4-e5f6-4789-a012-3456789abcde" || e.Account != "" {
		t.Errorf("balance_uid = %q account = %q (account filled later)", e.BalanceUID, e.Account)
	}
	if e.Note != "lunch" || e.SpentAt != "2026-10-06T21:06:00+08:00" {
		t.Errorf("bad trim/spent_at: %+v", e)
	}
}

func TestValidateReportsAllFields(t *testing.T) {
	_, err := CreateInput{Amount: "abc", Currency: "XYZ", SpentAt: "2026-10-06T21:06:00"}.Validate()
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %v", err)
	}
	for _, f := range []string{"currency", "balance_uid", "spent_at"} {
		if _, ok := ve.Fields[f]; !ok {
			t.Errorf("expected error for field %q, got %v", f, ve.Fields)
		}
	}

	_, err = CreateInput{Amount: "1.5", Currency: "JPY", BalanceUID: "not-a-uuid", SpentAt: "2026-10-06T21:06:00+09:00"}.Validate()
	if !errors.As(err, &ve) || ve.Fields["amount"] == "" || ve.Fields["balance_uid"] == "" {
		t.Errorf("expected amount + balance_uid errors, got %v", err)
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

func TestAttachPaymentBalance(t *testing.T) {
	e, err := CreateInput{
		Amount: "10", Currency: "THB", BalanceUID: "11111111-1111-4111-8111-111111111111",
		SpentAt: "2026-10-06T21:06:00+08:00",
	}.Validate()
	if err != nil {
		t.Fatal(err)
	}
	ok := balance.Balance{UID: "11111111-1111-4111-8111-111111111111", Name: "KBank debit", Type: balance.PaymentAccount}
	if err := e.AttachPaymentBalance(ok); err != nil || e.Account != "KBank debit" {
		t.Errorf("attach: %+v %v", e, err)
	}
	card := balance.Balance{UID: "11111111-1111-4111-8111-111111111111", Name: "Visa", Type: balance.CreditCard}
	if err := e.AttachPaymentBalance(card); err != nil || e.Account != "Visa" {
		t.Errorf("card: %+v %v", e, err)
	}
	asset := balance.Balance{UID: "11111111-1111-4111-8111-111111111111", Name: "Gold", Type: balance.OtherAsset}
	var ve *ValidationError
	if err := e.AttachPaymentBalance(asset); !errors.As(err, &ve) || ve.Fields["balance_uid"] == "" {
		t.Errorf("other_asset should fail: %v", err)
	}
	_ = money.FormatAmount // silence if unused in future
}

func TestIsPayableType(t *testing.T) {
	if !IsPayableType(balance.PaymentAccount) || !IsPayableType(balance.CreditCard) {
		t.Fatal("expected payable")
	}
	if IsPayableType(balance.OtherAsset) || IsPayableType(balance.OtherLiability) {
		t.Fatal("expected not payable")
	}
}
