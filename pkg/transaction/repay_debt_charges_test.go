package transaction

import (
	"strings"
	"testing"

	"github.com/nutmos/expensetrackr/pkg/category"
)

func TestRepayDebtCharges(t *testing.T) {
	in := repayInput()
	in.InterestAmount, in.FeeAmount, in.CategoryUID = " 12.5 ", "3", roiCatUID
	e, err := in.Validate()
	if err != nil {
		t.Fatal(err)
	}
	if e.InterestAmount == nil || *e.InterestAmount != "12.50" || *e.InterestAmountMinor != 1250 ||
		e.FeeAmount == nil || *e.FeeAmount != "3.00" || *e.FeeAmountMinor != 300 {
		t.Fatalf("charges = %v %v %v %v", e.InterestAmount, e.InterestAmountMinor, e.FeeAmount, e.FeeAmountMinor)
	}
	if e.ChargesMinor() != 1550 || e.CategoryUID == nil {
		t.Fatalf("ChargesMinor = %d, category = %v", e.ChargesMinor(), e.CategoryUID)
	}
	effects := e.Effects()
	if len(effects) != 2 || effects[0].Delta != -(25000+1550) || effects[1].Delta != 25000 {
		t.Errorf("effects = %+v, want account -(principal+charges), debt -principal", effects)
	}

	// The charges' category must be an expense category.
	if err := e.AttachCategory(category.Category{UID: UID(roiCatUID), Type: category.Type("income")}); err == nil {
		t.Error("AttachCategory(income) = nil, want error")
	}
	if err := e.AttachCategory(category.Category{UID: UID(roiCatUID), Type: category.Type("expense")}); err != nil {
		t.Errorf("AttachCategory(expense) = %v", err)
	}
}

func TestRepayDebtChargesZeroIsNone(t *testing.T) {
	in := repayInput()
	in.InterestAmount, in.FeeAmount = "0", "0.00"
	e, err := in.Validate()
	if err != nil {
		t.Fatal(err)
	}
	if e.InterestAmount != nil || e.FeeAmount != nil || e.ChargesMinor() != 0 {
		t.Errorf("zero charges stored: %v %v", e.InterestAmount, e.FeeAmount)
	}
	if e.Effects()[0].Delta != -e.AmountMinor {
		t.Errorf("effects = %+v", e.Effects())
	}
}

func TestRepayDebtChargesRejects(t *testing.T) {
	cases := []struct {
		name  string
		edit  func(*CreateInput)
		field string
		want  string
	}{
		{"negative interest", func(in *CreateInput) { in.InterestAmount = "-1" }, "interest_amount", "0 or more"},
		{"bad fee", func(in *CreateInput) { in.FeeAmount = "1,00" }, "fee_amount", ""},
		{"too many decimals", func(in *CreateInput) { in.FeeAmount = "1.005" }, "fee_amount", "decimal places"},
		{"interest on expense", func(in *CreateInput) {
			in.Type, in.ToBalanceUID, in.CategoryUID, in.InterestAmount = "expense", "", roiCatUID, "1"
		}, "interest_amount", "only allowed when type is repay_debt"},
		{"fee on transfer", func(in *CreateInput) { in.Type, in.FeeAmount = "transfer", "1" }, "fee_amount", "only allowed when type is repay_debt"},
		{"category with zero charges", func(in *CreateInput) { in.InterestAmount, in.CategoryUID = "0", roiCatUID }, "category_uid", "only allowed for a repay debt with"},
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
