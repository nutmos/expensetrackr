package transaction

import (
	"strings"
	"testing"

	"github.com/nutmos/expensetrackr/pkg/balance"
)

func repayInput() CreateInput {
	return CreateInput{Type: " Repay_Debt ", Amount: "250", Currency: "thb", BalanceUID: roiWalletUID, ToBalanceUID: roiGoldUID, SpentAt: roiSpentAt}
}

func TestRepayDebtParse(t *testing.T) {
	if got, ok := ParseType("REPAY_DEBT"); !ok || got != RepayDebt {
		t.Errorf("ParseType = %q, %v", got, ok)
	}
	if got, ok := ParseFilterType("repay_debt"); !ok || got != RepayDebt {
		t.Errorf("ParseFilterType = %q, %v", got, ok)
	}
	if !RepayDebt.HasDestination() {
		t.Error("HasDestination = false")
	}
	if RepayDebt.NeedsCategory() {
		t.Error("NeedsCategory = true")
	}
}

func TestRepayDebtAllowedBalances(t *testing.T) {
	cases := []struct {
		bt        balance.Type
		src, dest bool
	}{
		{balance.TypePaymentAccount, true, false},
		{balance.TypeCreditCard, false, true},
		{balance.TypeOtherAsset, false, false},
		{balance.TypeOtherLiability, false, true},
	}
	for _, tc := range cases {
		if got := SourceAllowed(RepayDebt, tc.bt); got != tc.src {
			t.Errorf("SourceAllowed(%s) = %v", tc.bt, got)
		}
		if got := DestinationAllowed(RepayDebt, tc.bt); got != tc.dest {
			t.Errorf("DestinationAllowed(%s) = %v", tc.bt, got)
		}
	}
}

func TestRepayDebtValidate(t *testing.T) {
	e, err := repayInput().Validate()
	if err != nil {
		t.Fatal(err)
	}
	if e.Type != RepayDebt || e.ToBalanceUID == nil || e.CategoryUID != nil || e.Currency != "THB" {
		t.Fatalf("validated: %+v", e)
	}
	effects := e.Effects()
	if len(effects) != 2 ||
		effects[0].Field != "balance_uid" || effects[0].Delta != -e.AmountMinor ||
		effects[1].Field != "to_balance_uid" || effects[1].Delta != e.AmountMinor || effects[1].Currency != "THB" {
		t.Errorf("effects = %+v, want -amount on balance_uid, +amount on to_balance_uid", effects)
	}

	cases := []struct {
		name  string
		edit  func(*CreateInput)
		field string
		want  string
	}{
		{"missing to_balance_uid", func(in *CreateInput) { in.ToBalanceUID = "" }, "to_balance_uid", "is required for a repay debt"},
		{"same as balance_uid", func(in *CreateInput) { in.ToBalanceUID = roiWalletUID }, "to_balance_uid", "must differ"},
		{"category not allowed", func(in *CreateInput) { in.CategoryUID = roiCatUID }, "category_uid", "is not allowed for a repay debt"},
		{"to_amount not allowed", func(in *CreateInput) { in.ToAmount = "1" }, "to_amount", "only allowed when type is transfer"},
		{"to_currency not allowed", func(in *CreateInput) { in.ToCurrency = "USD" }, "to_currency", "only allowed when type is transfer"},
		{"unknown type lists repay_debt", func(in *CreateInput) { in.Type = "repay" }, "type", "repay_debt"},
		{"to_balance_uid on expense", func(in *CreateInput) { in.Type = "expense"; in.CategoryUID = roiCatUID }, "to_balance_uid", "repay_debt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := repayInput()
			tc.edit(&in)
			_, err := in.Validate()
			if msg := fieldMsg(t, err, tc.field); !strings.Contains(msg, tc.want) {
				t.Errorf("%s = %q, want containing %q", tc.field, msg, tc.want)
			}
		})
	}
}

func TestRepayDebtAttach(t *testing.T) {
	mk := func(uid string, bt balance.Type) balance.Balance {
		return balance.Balance{UID: UID(uid), Name: "Balance", Type: bt}
	}
	for _, bt := range []balance.Type{balance.TypePaymentAccount, balance.TypeCreditCard, balance.TypeOtherAsset, balance.TypeOtherLiability} {
		e, _ := repayInput().Validate()
		err := e.AttachPaymentBalance(mk(roiWalletUID, bt))
		if ok := bt == balance.TypePaymentAccount; (err == nil) != ok {
			t.Errorf("AttachPaymentBalance(%s) err = %v", bt, err)
		}
		if err != nil && !strings.Contains(fieldMsg(t, err, "balance_uid"), "must refer to a payment_account balance") {
			t.Errorf("balance_uid message: %v", err)
		}

		e, _ = repayInput().Validate()
		err = e.AttachDestinationBalance(mk(roiGoldUID, bt))
		if ok := bt == balance.TypeCreditCard || bt == balance.TypeOtherLiability; (err == nil) != ok {
			t.Errorf("AttachDestinationBalance(%s) err = %v", bt, err)
		}
		if err != nil && !strings.Contains(fieldMsg(t, err, "to_balance_uid"), "credit_card or other_liability") {
			t.Errorf("to_balance_uid message: %v", err)
		}
	}
}
