package balance

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/nutmos/expensetrackr/pkg/money"
	"github.com/nutmos/expensetrackr/pkg/validate"
)

func dec(s string) *money.DecimalInput { d := money.DecimalInput(s); return &d }

func fieldErrs(t *testing.T, err error) map[string]string {
	t.Helper()
	var ve *validate.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %v", err)
	}
	return ve.Fields
}

func TestValidateAssetTypes(t *testing.T) {
	for _, typ := range []string{"payment_account", "other_asset", "Payment_Account"} {
		b, err := Input{Name: " KBank debit ", Type: typ, Currency: "thb", Balance: dec("-35.5"), Description: " main "}.Validate()
		if err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		if b.Name != "KBank debit" || b.Currency != "THB" || b.Kind != "asset" || b.Description != "main" ||
			*b.Balance != "-35.50" || *b.BalanceMinor != -3550 || b.Debt != nil || b.Limit != nil || b.Available != nil || b.OverLimit != nil {
			t.Errorf("%s: unexpected %+v", typ, b)
		}
	}
	// Zero balance is fine.
	if _, err := (Input{Name: "Wallet", Type: "other_asset", Currency: "JPY", Balance: dec("0")}).Validate(); err != nil {
		t.Errorf("zero balance: %v", err)
	}
	// Missing balance; debt/limit not allowed.
	f := fieldErrs(t, func() error {
		_, err := Input{Name: "x", Type: "payment_account", Currency: "THB", Debt: dec("1"), Limit: dec("2")}.Validate()
		return err
	}())
	if f["balance"] == "" || f["debt"] == "" || f["limit"] == "" {
		t.Errorf("asset field errors: %v", f)
	}
}

func TestValidateLiabilityTypes(t *testing.T) {
	for _, typ := range []string{"credit_card", "other_liability"} {
		b, err := Input{Name: "UOB One", Type: typ, Currency: "SGD", Debt: dec("250.4"), Limit: dec("5000")}.Validate()
		if err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		if b.Kind != "liability" || *b.Debt != "250.40" || *b.Limit != "5000.00" || *b.Available != "4749.60" ||
			*b.AvailableMinor != 474960 || *b.OverLimit || b.Balance != nil {
			t.Errorf("%s: unexpected %+v", typ, b)
		}
	}
	// Debt above limit is allowed but flagged.
	b, err := Input{Name: "Card", Type: "credit_card", Currency: "THB", Debt: dec("1200"), Limit: dec("1000")}.Validate()
	if err != nil || !*b.OverLimit || *b.Available != "-200.00" {
		t.Errorf("over limit: %+v %v", b, err)
	}
	// Negative debt (overpaid, in credit) is allowed.
	if b, err := (Input{Name: "Card", Type: "credit_card", Currency: "THB", Debt: dec("-25.5"), Limit: dec("1000")}).Validate(); err != nil || *b.Debt != "-25.50" || *b.Available != "1025.50" || *b.OverLimit {
		t.Errorf("negative debt: %+v %v", b, err)
	}
	// Zero debt / zero limit are allowed.
	if _, err := (Input{Name: "Loan", Type: "other_liability", Currency: "THB", Debt: dec("0"), Limit: dec("0")}).Validate(); err != nil {
		t.Errorf("zero debt/limit: %v", err)
	}
	f := fieldErrs(t, func() error {
		_, err := Input{Name: "Card", Type: "credit_card", Currency: "THB", Balance: dec("5"), Debt: dec("x")}.Validate()
		return err
	}())
	if f["debt"] == "" || f["limit"] == "" || f["balance"] == "" {
		t.Errorf("liability field errors: %v", f)
	}
}

func TestValidateCommonFields(t *testing.T) {
	f := fieldErrs(t, func() error { _, err := Input{}.Validate(); return err }())
	for _, k := range []string{"name", "type", "currency"} {
		if f[k] == "" {
			t.Errorf("expected %s error, got %v", k, f)
		}
	}
	f = fieldErrs(t, func() error {
		_, err := Input{Name: "x", Type: "wallet", Currency: "XYZ", Balance: dec("1")}.Validate()
		return err
	}())
	if f["type"] == "" || f["currency"] == "" {
		t.Errorf("bad type/currency: %v", f)
	}
	// Decimal places checked against the currency.
	f = fieldErrs(t, func() error {
		_, err := Input{Name: "x", Type: "payment_account", Currency: "JPY", Balance: dec("10.5")}.Validate()
		return err
	}())
	if f["balance"] == "" {
		t.Errorf("JPY decimals: %v", f)
	}
	// Empty-string amounts count as not provided (HTML forms).
	if _, err := (Input{Name: "x", Type: "payment_account", Currency: "THB", Balance: dec("1"), Debt: dec(""), Limit: dec(" ")}).Validate(); err != nil {
		t.Errorf("empty inapplicable amounts should be ignored: %v", err)
	}
}

func TestJSONNullAmountIsAbsent(t *testing.T) {
	var in Input
	if err := json.Unmarshal([]byte(`{"name":"x","type":"payment_account","currency":"THB","balance":12.5,"debt":null,"limit":null}`), &in); err != nil {
		t.Fatal(err)
	}
	if string(*in.Balance) != "12.5" {
		t.Errorf("balance literal = %q", *in.Balance)
	}
	if _, err := in.Validate(); err != nil {
		t.Errorf("null debt/limit should be accepted for assets: %v", err)
	}
}

func stored(t *testing.T, in Input) Balance {
	t.Helper()
	b, err := in.Validate()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestComputeTotals(t *testing.T) {
	items := []Balance{
		stored(t, Input{Name: "a", Type: "payment_account", Currency: "THB", Balance: dec("1000.50")}),
		stored(t, Input{Name: "b", Type: "other_asset", Currency: "THB", Balance: dec("-0.50")}),
		stored(t, Input{Name: "c", Type: "credit_card", Currency: "THB", Debt: dec("300"), Limit: dec("5000")}),
		stored(t, Input{Name: "d", Type: "other_liability", Currency: "THB", Debt: dec("200"), Limit: dec("100")}),
		stored(t, Input{Name: "e", Type: "payment_account", Currency: "JPY", Balance: dec("5000")}),
	}
	got := ComputeTotals(items)
	if len(got) != 2 || got[0].Currency != "JPY" || got[1].Currency != "THB" {
		t.Fatalf("totals: %+v", got)
	}
	thb := got[1]
	if thb.Assets != "1000.00" || thb.Liabilities != "500.00" || thb.Net != "500.00" || thb.CreditLimit != "5100.00" || thb.AvailableCredit != "4600.00" {
		t.Errorf("THB totals: %+v", thb)
	}
	if got[0].Assets != "5000" || got[0].Liabilities != "0" || got[0].Net != "5000" {
		t.Errorf("JPY totals: %+v", got[0])
	}
}
