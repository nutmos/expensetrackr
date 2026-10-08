package balance

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"expense-service/internal/money"
	"expense-service/internal/validate"
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
	// Zero debt / zero limit are allowed.
	if _, err := (Input{Name: "Loan", Type: "other_liability", Currency: "THB", Debt: dec("0"), Limit: dec("0")}).Validate(); err != nil {
		t.Errorf("zero debt/limit: %v", err)
	}
	f := fieldErrs(t, func() error {
		_, err := Input{Name: "Card", Type: "credit_card", Currency: "THB", Balance: dec("5"), Debt: dec("-1")}.Validate()
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

func patchOf(t *testing.T, s string) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func stored(t *testing.T, in Input) Balance {
	t.Helper()
	b, err := in.Validate()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestApplyPatch(t *testing.T) {
	acct := stored(t, Input{Name: "KBank", Type: "payment_account", Currency: "THB", Balance: dec("100.50"), Description: "d"})

	in := acct.Input()
	if err := in.ApplyPatch(patchOf(t, `{"balance":"-20","description":null}`)); err != nil {
		t.Fatal(err)
	}
	b, err := in.Validate()
	if err != nil || *b.Balance != "-20.00" || b.Description != "" || b.Name != "KBank" {
		t.Errorf("simple patch: %+v %v", b, err)
	}

	// type may be sent in a patch, but ValidateUpdate only accepts the stored one.
	in = acct.Input()
	if err := in.ApplyPatch(patchOf(t, `{"type":" Payment_Account ","balance":"1"}`)); err != nil {
		t.Fatal(err)
	}
	if b, err := in.ValidateUpdate(acct.Type); err != nil || *b.Balance != "1.00" || b.Type != PaymentAccount {
		t.Errorf("same type: %+v %v", b, err)
	}
	for _, patch := range []string{
		`{"type":"credit_card"}`,
		`{"type":"credit_card","debt":"10","limit":"100"}`,
		`{"type":"other_asset"}`,
		`{"type":"bogus"}`,
	} {
		in = acct.Input()
		if err := in.ApplyPatch(patchOf(t, patch)); err != nil {
			t.Fatalf("%s: %v", patch, err)
		}
		_, err := in.ValidateUpdate(acct.Type)
		if f := fieldErrs(t, err); f["type"] != ErrTypeChanged {
			t.Errorf("%s: want type error, got %v", patch, f)
		}
		// Amounts are judged against the stored type: debt/limit are not used by
		// payment_account, the existing balance is still fine.
		if f := fieldErrs(t, err); strings.Contains(patch, "debt") != (f["debt"] != "") || f["balance"] != "" {
			t.Errorf("%s: other fields: %v", patch, f)
		}
	}
	// Currency change re-scales: THB 100.50 -> JPY rejected, -> KWD 100.500.
	in = acct.Input()
	_ = in.ApplyPatch(patchOf(t, `{"currency":"JPY"}`))
	if _, err := in.Validate(); fieldErrs(t, err)["balance"] == "" {
		t.Errorf("THB->JPY should fail on balance")
	}
	in = acct.Input()
	_ = in.ApplyPatch(patchOf(t, `{"currency":"KWD"}`))
	if b, err := in.Validate(); err != nil || *b.Balance != "100.500" || *b.BalanceMinor != 100500 {
		t.Errorf("THB->KWD: %+v %v", b, err)
	}

	var re *validate.RequestError
	for _, bad := range []string{`{}`, `{"foo":1}`, `{"name":5}`} {
		in = acct.Input()
		if err := in.ApplyPatch(patchOf(t, bad)); !errors.As(err, &re) {
			t.Errorf("%s: expected RequestError, got %v", bad, err)
		}
	}
	// A non-numeric literal is kept verbatim and rejected by Validate (422), as for expenses.
	in = acct.Input()
	if err := in.ApplyPatch(patchOf(t, `{"balance":true}`)); err != nil {
		t.Errorf("literal true: %v", err)
	} else if _, err := in.Validate(); fieldErrs(t, err)["balance"] == "" {
		t.Errorf("balance true should fail validation")
	}
	in = acct.Input()
	if err := in.ApplyPatch(patchOf(t, `{"name":null,"type":null}`)); len(fieldErrs(t, err)) != 2 {
		t.Errorf("null name/type: %v", err)
	}
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
