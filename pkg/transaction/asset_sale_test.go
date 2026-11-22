package transaction

import (
	"strings"
	"testing"

	"github.com/nutmos/expensetrackr/pkg/balance"
	"github.com/nutmos/expensetrackr/pkg/category"
)

func saleInput() CreateInput {
	return CreateInput{Type: " Asset_Sale ", Amount: "250", Currency: "thb", BalanceUID: roiWalletUID, ToBalanceUID: roiGoldUID, SpentAt: roiSpentAt}
}

func TestAssetSaleParse(t *testing.T) {
	if got, ok := ParseType("ASSET_SALE"); !ok || got != AssetSale {
		t.Errorf("ParseType = %q, %v", got, ok)
	}
	if got, ok := ParseFilterType("asset_sale"); !ok || got != AssetSale {
		t.Errorf("ParseFilterType = %q, %v", got, ok)
	}
	if !AssetSale.HasDestination() || AssetSale.NeedsCategory() || AssetSale.AllowsForeignAmount() || !AssetSale.AllowsSaleResult() {
		t.Error("AssetSale flags wrong")
	}
	for bt, want := range map[balance.Type]bool{balance.TypePaymentAccount: true, balance.TypeCreditCard: true, balance.TypeOtherAsset: false, balance.TypeOtherLiability: false} {
		if SourceAllowed(AssetSale, bt) != want {
			t.Errorf("SourceAllowed(%s) != %v", bt, want)
		}
		if DestinationAllowed(AssetSale, bt) != (bt == balance.TypeOtherAsset) {
			t.Errorf("DestinationAllowed(%s) wrong", bt)
		}
	}
}

func TestAssetSaleGain(t *testing.T) {
	in := saleInput()
	in.GainLoss, in.CategoryUID, in.EntireAssetSale = " 40.5 ", roiCatUID, true
	e, err := in.Validate()
	if err != nil {
		t.Fatal(err)
	}
	if e.GainLoss == nil || *e.GainLoss != "40.50" || *e.GainLossMinor != 4050 || !e.EntireAssetSale || e.CategoryUID == nil {
		t.Fatalf("sale = %v %v %v %v", e.GainLoss, e.GainLossMinor, e.EntireAssetSale, e.CategoryUID)
	}
	effects := e.Effects()
	if len(effects) != 2 || effects[0].Delta != 25000+4050 || effects[1].Delta != -25000 || effects[1].Field != "to_balance_uid" {
		t.Errorf("effects = %+v, want account +(principal+gain), asset -principal", effects)
	}
	// A gain's category must be an income category.
	if err := e.AttachCategory(category.Category{UID: UID(roiCatUID), Type: category.Type("expense")}); err == nil {
		t.Error("AttachCategory(expense) on a gain = nil, want error")
	}
	if err := e.AttachCategory(category.Category{UID: UID(roiCatUID), Type: category.Type("income")}); err != nil {
		t.Errorf("AttachCategory(income) = %v", err)
	}
}

func TestAssetSaleLoss(t *testing.T) {
	in := saleInput()
	in.GainLoss, in.CategoryUID = "-250", roiCatUID // everything lost: proceeds 0
	e, err := in.Validate()
	if err != nil {
		t.Fatal(err)
	}
	if e.GainLoss == nil || *e.GainLoss != "-250.00" || e.GainLossValue() != -25000 {
		t.Fatalf("gain_loss = %v", e.GainLoss)
	}
	if ef := e.Effects(); ef[0].Delta != 0 || ef[1].Delta != -25000 {
		t.Errorf("effects = %+v", ef)
	}
	// A loss's category must be an expense category.
	if err := e.AttachCategory(category.Category{UID: UID(roiCatUID), Type: category.Type("income")}); err == nil {
		t.Error("AttachCategory(income) on a loss = nil, want error")
	}
	if err := e.AttachCategory(category.Category{UID: UID(roiCatUID), Type: category.Type("expense")}); err != nil {
		t.Errorf("AttachCategory(expense) = %v", err)
	}
}

func TestAssetSaleNoResult(t *testing.T) {
	in := saleInput()
	in.GainLoss = "-0.00"
	e, err := in.Validate()
	if err != nil {
		t.Fatal(err)
	}
	if e.GainLoss != nil || e.EntireAssetSale || e.Effects()[0].Delta != e.AmountMinor {
		t.Errorf("zero gain_loss stored: %v %+v", e.GainLoss, e.Effects())
	}
}

func TestAssetSaleRejects(t *testing.T) {
	cases := []struct {
		name  string
		edit  func(*CreateInput)
		field string
		want  string
	}{
		{"bad gain_loss", func(in *CreateInput) { in.GainLoss = "1,00" }, "gain_loss", "plain decimal"},
		{"loss above principal", func(in *CreateInput) { in.GainLoss = "-250.01" }, "gain_loss", "larger than the principal"},
		{"no asset", func(in *CreateInput) { in.ToBalanceUID = "" }, "to_balance_uid", "is required for an asset sale"},
		{"category without result", func(in *CreateInput) { in.CategoryUID = roiCatUID }, "category_uid", "only allowed for an asset sale with"},
		{"foreign amount", func(in *CreateInput) { in.ForeignAmount, in.ForeignCurrency = "10", "USD" }, "foreign_amount", "only allowed"},
		{"gain on income", func(in *CreateInput) {
			in.Type, in.ToBalanceUID, in.CategoryUID, in.GainLoss = "income", "", roiCatUID, "1"
		}, "gain_loss", "only allowed when type is asset_sale"},
		{"entire flag on transfer", func(in *CreateInput) { in.Type, in.EntireAssetSale = "transfer", true }, "entire_asset_sale", "only allowed when type is asset_sale"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := saleInput()
			tc.edit(&in)
			_, err := in.Validate()
			if msg := fieldMsg(t, err, tc.field); !strings.Contains(msg, tc.want) {
				t.Errorf("%s = %q, want containing %q", tc.field, msg, tc.want)
			}
		})
	}
}
