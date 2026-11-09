package transaction

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/nutmos/expensetrackr/pkg/balance"
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
		Amount:      "120.5",
		Currency:    " thb ",
		BalanceUID:  "  A1B2C3D4-E5F6-4789-A012-3456789ABCDE ",
		CategoryUID: "CCCCCCCC-0000-4000-8000-000000000001",
		SpentAt:     "2026-10-06T21:06:00.999+08:00",
		Note:        " lunch ",
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

// testCategoryUID is any well-formed category uid (Validate checks only the
// shape; the API checks it against the user's categories).
const testCategoryUID = "cccccccc-0000-4000-8000-000000000001"

// A category is required for every type except transfer; balance
// adjustments can't be created here at all.
func TestValidateCategoryRequired(t *testing.T) {
	const bal, dest = "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
	for _, c := range []struct {
		typ, to, cat, want string
	}{
		{"", "", "", "is required for an expense"},
		{"expense", "", "  ", "is required for an expense"},
		{"income", "", "", "is required for an income"},
		{"asset_purchase", dest, "", "is required for an asset purchase"},
		{"return_on_investment", dest, "", "is required for a return on investment"},
		{"transfer", dest, "", ""},
		{"expense", "", testCategoryUID, ""},
		{"transfer", dest, testCategoryUID, "is not allowed for a transfer"},
	} {
		_, err := CreateInput{Type: c.typ, Amount: "1", Currency: "THB", BalanceUID: bal, ToBalanceUID: c.to,
			CategoryUID: c.cat, SpentAt: "2026-10-06T21:06:00+08:00"}.Validate()
		var got string
		var ve *ValidationError
		if errors.As(err, &ve) {
			got = ve.Fields["category_uid"]
		} else if err != nil {
			t.Fatalf("%s: %v", c.typ, err)
		}
		if got != c.want {
			t.Errorf("type %q category %q: category_uid error %q, want %q", c.typ, c.cat, got, c.want)
		}
	}
	for tt, want := range map[Type]bool{Expense: true, Income: true, AssetPurchase: true, ReturnOnInvestment: true, Transfer: false, BalanceAdjustment: false} {
		if tt.NeedsCategory() != want {
			t.Errorf("%s.NeedsCategory() = %v", tt, !want)
		}
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
		CategoryUID: testCategoryUID, SpentAt: "2026-10-06T21:06:00+08:00",
	}.Validate()
	if err != nil {
		t.Fatal(err)
	}
	ok := balance.Balance{UID: "11111111-1111-4111-8111-111111111111", Name: "KBank debit", Type: balance.TypePaymentAccount}
	if err := e.AttachPaymentBalance(ok); err != nil || e.Account != "KBank debit" {
		t.Errorf("attach: %+v %v", e, err)
	}
	card := balance.Balance{UID: "11111111-1111-4111-8111-111111111111", Name: "Visa", Type: balance.TypeCreditCard}
	if err := e.AttachPaymentBalance(card); err != nil || e.Account != "Visa" {
		t.Errorf("card: %+v %v", e, err)
	}
	asset := balance.Balance{UID: "11111111-1111-4111-8111-111111111111", Name: "Gold", Type: balance.TypeOtherAsset}
	var ve *ValidationError
	if err := e.AttachPaymentBalance(asset); !errors.As(err, &ve) || ve.Fields["balance_uid"] == "" {
		t.Errorf("other_asset should fail: %v", err)
	}
}

func TestIsPayableType(t *testing.T) {
	if !IsPayableType(balance.TypePaymentAccount) || !IsPayableType(balance.TypeCreditCard) {
		t.Fatal("expected payable")
	}
	if IsPayableType(balance.TypeOtherAsset) || IsPayableType(balance.TypeOtherLiability) {
		t.Fatal("expected not payable")
	}
}

func TestSourceAllowed(t *testing.T) {
	cases := []struct {
		name string
		tt   Type
		bt   balance.Type
		want bool
	}{
		{"expense payment_account", Expense, balance.TypePaymentAccount, true},
		{"expense credit_card", Expense, balance.TypeCreditCard, true},
		{"expense other_asset", Expense, balance.TypeOtherAsset, false},
		{"expense other_liability", Expense, balance.TypeOtherLiability, false},
		{"income payment_account", Income, balance.TypePaymentAccount, true},
		{"income other_asset", Income, balance.TypeOtherAsset, true},
		{"income credit_card", Income, balance.TypeCreditCard, true},
		{"income other_liability", Income, balance.TypeOtherLiability, false},
		{"asset_purchase credit_card", AssetPurchase, balance.TypeCreditCard, true},
		{"asset_purchase other_asset", AssetPurchase, balance.TypeOtherAsset, false},
		{"transfer other_liability", Transfer, balance.TypeOtherLiability, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := SourceAllowed(c.tt, c.bt); got != c.want {
				t.Fatalf("SourceAllowed(%s, %s) = %v, want %v", c.tt, c.bt, got, c.want)
			}
		})
	}
}

func TestAttachPaymentBalanceIncome(t *testing.T) {
	mk := func(t *testing.T) Transaction {
		t.Helper()
		e, err := CreateInput{
			Type: "income", Amount: "10", Currency: "THB",
			BalanceUID:  "11111111-1111-4111-8111-111111111111",
			CategoryUID: testCategoryUID,
			SpentAt:     "2026-10-06T21:06:00+08:00",
		}.Validate()
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	cases := []struct {
		name    string
		bt      balance.Type
		wantErr bool
	}{
		{"payment_account", balance.TypePaymentAccount, false},
		{"other_asset", balance.TypeOtherAsset, false},
		{"credit_card", balance.TypeCreditCard, false},
		{"other_liability", balance.TypeOtherLiability, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := mk(t)
			b := balance.Balance{UID: "11111111-1111-4111-8111-111111111111", Name: c.name, Type: c.bt}
			err := e.AttachPaymentBalance(b)
			if c.wantErr {
				var ve *ValidationError
				if !errors.As(err, &ve) || ve.Fields["balance_uid"] == "" {
					t.Fatalf("want balance_uid error, got %v", err)
				}
				if got := ve.Fields["balance_uid"]; got != errSourceIncome {
					t.Fatalf("message = %q, want %q", got, errSourceIncome)
				}
				return
			}
			if err != nil || e.Account != c.name {
				t.Fatalf("attach: account=%q err=%v", e.Account, err)
			}
		})
	}
}
