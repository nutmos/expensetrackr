package transaction

import (
	"strings"
	"testing"
)

func foreignInput(typ, amt, cur string) CreateInput {
	in := CreateInput{Type: typ, Amount: "850", Currency: "THB", BalanceUID: roiWalletUID, CategoryUID: roiCatUID, SpentAt: roiSpentAt,
		ForeignAmount: DecimalInput(amt), ForeignCurrency: cur}
	if typ == "transfer" || typ == "repay_debt" || typ == "asset_purchase" || typ == "return_on_investment" {
		in.ToBalanceUID = roiGoldUID
	}
	if typ == "transfer" || typ == "repay_debt" {
		in.CategoryUID = ""
	}
	return in
}

func TestForeignAmountValidate(t *testing.T) {
	for _, typ := range []string{"expense", "income", "asset_purchase", "return_on_investment"} {
		e, err := foreignInput(typ, " 25.5 ", " usd ").Validate()
		if err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		if e.ForeignAmount == nil || *e.ForeignAmount != "25.50" || *e.ForeignAmountMinor != 2550 || *e.ForeignCurrency != "USD" {
			t.Errorf("%s: foreign = %v %v %v", typ, e.ForeignAmount, e.ForeignAmountMinor, e.ForeignCurrency)
		}
		// Balances only ever move by amount in the transaction currency.
		for _, ef := range e.Effects() {
			if ef.Currency != "" && ef.Currency != "THB" {
				t.Errorf("%s: effect in %s", typ, ef.Currency)
			}
		}
	}
	e, err := foreignInput("expense", "1500", "JPY").Validate()
	if err != nil || *e.ForeignAmount != "1500" || *e.ForeignAmountMinor != 1500 {
		t.Errorf("JPY: %v %+v", err, e)
	}
	e, err = foreignInput("expense", "", "").Validate()
	if err != nil || e.ForeignAmount != nil || e.ForeignAmountMinor != nil || e.ForeignCurrency != nil {
		t.Errorf("none: %v %+v", err, e)
	}
}

func TestForeignAmountRejects(t *testing.T) {
	cases := []struct {
		name string
		in   CreateInput
		want map[string]string // field -> substring
	}{
		{"amount without currency", foreignInput("expense", "10", ""), map[string]string{"foreign_currency": "is required"}},
		{"currency without amount", foreignInput("expense", "", "USD"), map[string]string{"foreign_amount": "is required"}},
		{"unknown currency", foreignInput("expense", "10", "XYZ"), map[string]string{"foreign_currency": "ISO 4217"}},
		{"same currency", foreignInput("expense", "10", "thb"), map[string]string{"foreign_currency": "must differ"}},
		{"zero", foreignInput("expense", "0", "USD"), map[string]string{"foreign_amount": "greater than zero"}},
		{"negative", foreignInput("income", "-1", "USD"), map[string]string{"foreign_amount": ""}},
		{"too many decimals", foreignInput("expense", "10.5", "JPY"), map[string]string{"foreign_amount": ""}},
		{"transfer", foreignInput("transfer", "10", "USD"), map[string]string{"foreign_amount": "only allowed", "foreign_currency": "only allowed"}},
		{"repay_debt", foreignInput("repay_debt", "10", ""), map[string]string{"foreign_amount": "only allowed"}},
	}
	for _, tc := range cases {
		_, err := tc.in.Validate()
		ve, ok := err.(*ValidationError)
		if !ok {
			t.Errorf("%s: err = %v", tc.name, err)
			continue
		}
		for f, sub := range tc.want {
			if msg, ok := ve.Fields[f]; !ok || !strings.Contains(msg, sub) {
				t.Errorf("%s: %s = %q, want %q (all: %v)", tc.name, f, msg, sub, ve.Fields)
			}
		}
	}
}

func TestForeignAmountTypes(t *testing.T) {
	want := map[Type]bool{Expense: true, Income: true, AssetPurchase: true, ReturnOnInvestment: true, Transfer: false, RepayDebt: false, BalanceAdjustment: false}
	for typ, ok := range want {
		if typ.AllowsForeignAmount() != ok {
			t.Errorf("%s.AllowsForeignAmount() = %v", typ, !ok)
		}
	}
}
