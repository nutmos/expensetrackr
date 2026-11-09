package transaction

import (
	"errors"
	"strings"
	"testing"

	"github.com/nutmos/expensetrackr/pkg/balance"
	"github.com/nutmos/expensetrackr/pkg/category"
)

const (
	roiWalletUID = "11111111-1111-4111-8111-111111111111"
	roiGoldUID   = "22222222-2222-4222-8222-222222222222"
	roiCatUID    = "33333333-3333-4333-8333-333333333333"
	roiSpentAt   = "2026-10-08T12:00:00+08:00"
)

func roiInput() CreateInput {
	return CreateInput{Type: "Return_On_Investment", Amount: "12.50", Currency: "thb", BalanceUID: roiWalletUID, ToBalanceUID: roiGoldUID, CategoryUID: roiCatUID, SpentAt: roiSpentAt}
}

func fieldMsg(t *testing.T, err error, field string) string {
	t.Helper()
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want ValidationError, got %v", err)
	}
	return ve.Fields[field]
}

func TestReturnOnInvestmentParse(t *testing.T) {
	cases := []struct {
		in         string
		write, flt bool
	}{
		{"return_on_investment", true, true},
		{" RETURN_ON_INVESTMENT ", true, true},
		{"balance_adjustment", false, true},
		{"roi", false, false},
	}
	for _, tc := range cases {
		if got, ok := ParseType(tc.in); ok != tc.write || (ok && got != ReturnOnInvestment) {
			t.Errorf("ParseType(%q) = %q, %v", tc.in, got, ok)
		}
		if _, ok := ParseFilterType(tc.in); ok != tc.flt {
			t.Errorf("ParseFilterType(%q) ok = %v", tc.in, ok)
		}
	}
	if !ReturnOnInvestment.HasDestination() {
		t.Error("HasDestination = false")
	}
}

func TestReturnOnInvestmentAllowedBalances(t *testing.T) {
	cases := []struct {
		bt        balance.Type
		src, dest bool
	}{
		{balance.TypePaymentAccount, true, false},
		{balance.TypeCreditCard, true, false},
		{balance.TypeOtherAsset, false, true},
		{balance.TypeOtherLiability, false, false},
	}
	for _, tc := range cases {
		if got := SourceAllowed(ReturnOnInvestment, tc.bt); got != tc.src {
			t.Errorf("SourceAllowed(%s) = %v", tc.bt, got)
		}
		if got := DestinationAllowed(ReturnOnInvestment, tc.bt); got != tc.dest {
			t.Errorf("DestinationAllowed(%s) = %v", tc.bt, got)
		}
	}
}

func TestReturnOnInvestmentValidate(t *testing.T) {
	e, err := roiInput().Validate()
	if err != nil {
		t.Fatal(err)
	}
	if e.Type != ReturnOnInvestment || e.ToBalanceUID == nil || e.Currency != "THB" {
		t.Fatalf("validated: %+v", e)
	}
	effects := e.Effects()
	if len(effects) != 1 || effects[0].Field != "balance_uid" || effects[0].Delta != e.AmountMinor {
		t.Errorf("effects = %+v, want only +amount on balance_uid", effects)
	}

	cases := []struct {
		name  string
		edit  func(*CreateInput)
		field string
		want  string
	}{
		{"missing to_balance_uid", func(in *CreateInput) { in.ToBalanceUID = "" }, "to_balance_uid", "is required for a return on investment"},
		{"same as balance_uid", func(in *CreateInput) { in.ToBalanceUID = roiWalletUID }, "to_balance_uid", "must differ"},
		{"to_amount not allowed", func(in *CreateInput) { in.ToAmount = "1" }, "to_amount", "only allowed when type is transfer"},
		{"to_currency not allowed", func(in *CreateInput) { in.ToCurrency = "USD" }, "to_currency", "only allowed when type is transfer"},
		{"unknown type lists roi", func(in *CreateInput) { in.Type = "dividend" }, "type", "return_on_investment"},
		{"to_balance_uid on income", func(in *CreateInput) { in.Type = "income" }, "to_balance_uid", "return_on_investment"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := roiInput()
			tc.edit(&in)
			_, err := in.Validate()
			if msg := fieldMsg(t, err, tc.field); !strings.Contains(msg, tc.want) {
				t.Errorf("%s = %q, want containing %q", tc.field, msg, tc.want)
			}
		})
	}
}

func TestReturnOnInvestmentAttach(t *testing.T) {
	mk := func(uid string, bt balance.Type) balance.Balance {
		return balance.Balance{UID: UID(uid), Name: "Balance", Type: bt}
	}
	src := []struct {
		bt balance.Type
		ok bool
	}{
		{balance.TypePaymentAccount, true},
		{balance.TypeCreditCard, true},
		{balance.TypeOtherAsset, false},
		{balance.TypeOtherLiability, false},
	}
	for _, tc := range src {
		e, _ := roiInput().Validate()
		err := e.AttachPaymentBalance(mk(roiWalletUID, tc.bt))
		if (err == nil) != tc.ok {
			t.Errorf("AttachPaymentBalance(%s) err = %v", tc.bt, err)
		}
		if err != nil && !strings.Contains(fieldMsg(t, err, "balance_uid"), "return on investment") {
			t.Errorf("balance_uid message: %v", err)
		}
	}
	dest := []struct {
		bt balance.Type
		ok bool
	}{
		{balance.TypeOtherAsset, true},
		{balance.TypePaymentAccount, false},
		{balance.TypeCreditCard, false},
		{balance.TypeOtherLiability, false},
	}
	for _, tc := range dest {
		e, _ := roiInput().Validate()
		err := e.AttachDestinationBalance(mk(roiGoldUID, tc.bt))
		if (err == nil) != tc.ok {
			t.Errorf("AttachDestinationBalance(%s) err = %v", tc.bt, err)
		}
		if err != nil && !strings.Contains(fieldMsg(t, err, "to_balance_uid"), "investment this return is tied to") {
			t.Errorf("to_balance_uid message: %v", err)
		}
	}
	cats := []struct {
		ct category.Type
		ok bool
	}{
		{category.TypeIncome, true},
		{category.TypeExpense, false},
	}
	for _, tc := range cats {
		in := roiInput()
		in.CategoryUID = roiCatUID
		e, err := in.Validate()
		if err != nil {
			t.Fatal(err)
		}
		err = e.AttachCategory(category.Category{UID: UID(roiCatUID), Type: tc.ct})
		if (err == nil) != tc.ok {
			t.Errorf("AttachCategory(%s) err = %v", tc.ct, err)
		}
	}
}
