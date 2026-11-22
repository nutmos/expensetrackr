package transaction

import "testing"

func TestBalanceAdjustmentType(t *testing.T) {
	if _, ok := ParseType("balance_adjustment"); ok {
		t.Error("ParseType accepts balance_adjustment (clients must not write it)")
	}
	if typ, ok := ParseFilterType(" Balance_Adjustment "); !ok || typ != BalanceAdjustment {
		t.Errorf("ParseFilterType: %q %v", typ, ok)
	}
	if _, ok := ParseFilterType(""); ok {
		t.Error("ParseFilterType accepts empty")
	}
	in := CreateInput{Type: "balance_adjustment", Amount: "5", Currency: "THB", BalanceUID: "11111111-1111-4111-8111-111111111111", SpentAt: "2026-10-08T12:00:00+08:00"}
	_, err := in.Validate()
	if ve, ok := err.(*ValidationError); !ok || ve.Fields["type"] != ErrAdjustmentType {
		t.Errorf("Validate: %v", err)
	}
	// Effects are on the amount as shown: the store flips them for a liability.
	inc, dec := Increase, Decrease
	for _, c := range []struct {
		dir             *Direction
		asset, liabilty int64
	}{{&inc, 100, -100}, {&dec, -100, 100}} {
		eff := (Transaction{Type: BalanceAdjustment, AmountMinor: 100, BalanceUID: "x", AdjustmentDirection: c.dir}).Effects()
		if len(eff) != 1 || !eff[0].ByAmount || eff[0].BalanceUID != "x" ||
			eff[0].ValueDelta(false) != c.asset || eff[0].ValueDelta(true) != c.liabilty {
			t.Errorf("%s adjustment effects: %+v", *c.dir, eff)
		}
	}
	if d := (Effect{Delta: 5}).ValueDelta(true); d != 5 {
		t.Errorf("value effect on a liability: %d", d)
	}
}

func TestValidateAdjustment(t *testing.T) {
	str := func(s string) *string { return &s }
	base := func() CreateInput {
		return CreateInput{Amount: "12.5", Currency: "thb", BalanceUID: "11111111-1111-4111-8111-111111111111",
			SpentAt: "2026-10-08T12:00:00+08:00", Note: " fix ", AdjustmentDirection: str(" Decrease ")}
	}
	for _, typ := range []string{"", "balance_adjustment", " Balance_Adjustment "} {
		in := base()
		in.Type = typ
		e, err := in.ValidateAdjustment()
		if err != nil || e.Type != BalanceAdjustment || e.Amount != "12.50" || e.Currency != "THB" ||
			e.AdjustmentDirection == nil || *e.AdjustmentDirection != Decrease || e.Note != "fix" || e.CategoryUID != nil {
			t.Errorf("type %q: %+v %v", typ, e, err)
		}
	}
	fieldErr := func(name string, mut func(*CreateInput), field string) {
		t.Helper()
		in := base()
		mut(&in)
		_, err := in.ValidateAdjustment()
		if ve, ok := err.(*ValidationError); !ok || ve.Fields[field] == "" {
			t.Errorf("%s: want error on %s, got %v", name, field, err)
		}
	}
	fieldErr("other type", func(in *CreateInput) { in.Type = "expense" }, "type")
	fieldErr("no direction", func(in *CreateInput) { in.AdjustmentDirection = nil }, "adjustment_direction")
	fieldErr("empty direction", func(in *CreateInput) { in.AdjustmentDirection = str("") }, "adjustment_direction")
	fieldErr("bad direction", func(in *CreateInput) { in.AdjustmentDirection = str("up") }, "adjustment_direction")
	fieldErr("category", func(in *CreateInput) { in.CategoryUID = "22222222-2222-4222-8222-222222222222" }, "category_uid")
	fieldErr("destination", func(in *CreateInput) { in.ToBalanceUID = "22222222-2222-4222-8222-222222222222" }, "to_balance_uid")
	fieldErr("to_amount", func(in *CreateInput) { in.ToAmount = "1" }, "to_amount")
	fieldErr("foreign", func(in *CreateInput) { in.ForeignAmount, in.ForeignCurrency = "1", "USD" }, "foreign_amount")
	fieldErr("import_ref", func(in *CreateInput) { in.ImportRef = str("ref") }, "import_ref")
	fieldErr("amount", func(in *CreateInput) { in.Amount = "0" }, "amount")

	// Other types reject a direction.
	in := base()
	in.Type, in.CategoryUID = "expense", "22222222-2222-4222-8222-222222222222"
	if _, err := in.Validate(); err == nil || err.(*ValidationError).Fields["adjustment_direction"] == "" {
		t.Errorf("expense with direction: %v", err)
	}
	in.AdjustmentDirection = str("")
	if _, err := in.Validate(); err != nil {
		t.Errorf("expense with empty direction: %v", err)
	}
}

// Beginning balances: never created through Validate; ValidateBeginningBalance
// keeps the type, accepts 0 and requires a direction.
func TestValidateBeginningBalance(t *testing.T) {
	dir := "decrease"
	in := CreateInput{Type: "beginning_balance", Amount: "0", Currency: "THB", BalanceUID: "4dc73a4a-a60b-414e-98bc-3c71298a0af5",
		SpentAt: "2026-10-01T00:00:00+07:00", AdjustmentDirection: &dir}
	if _, err := in.Validate(); err == nil || err.(*ValidationError).Fields["type"] != ErrBeginningBalanceType {
		t.Errorf("Validate: %v", err)
	}
	e, err := in.ValidateBeginningBalance()
	if err != nil || e.Type != BeginningBalance || e.Amount != "0.00" || e.AdjustmentDirection == nil || *e.AdjustmentDirection != Decrease {
		t.Fatalf("ValidateBeginningBalance: %+v %v", e, err)
	}
	if ef := e.Effects(); len(ef) != 1 || !ef[0].ByAmount || ef[0].Delta != 0 {
		t.Errorf("effects %+v", ef)
	}
	in.Type = "expense"
	if _, err := in.ValidateBeginningBalance(); err == nil || err.(*ValidationError).Fields["type"] != ErrBeginningBalanceTypeChange {
		t.Errorf("type change: %v", err)
	}
	in.Type, in.AdjustmentDirection, in.Amount = "", nil, "-1"
	_, err = in.ValidateBeginningBalance()
	if f := err.(*ValidationError).Fields; f["adjustment_direction"] == "" || f["amount"] == "" {
		t.Errorf("missing direction / negative amount: %v", f)
	}
	in.Amount = "0"
	if _, err := in.ValidateAdjustment(); err == nil || err.(*ValidationError).Fields["amount"] == "" {
		t.Errorf("a balance adjustment still needs an amount > 0: %v", err)
	}
}
