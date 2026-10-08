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
	if eff := (Transaction{Type: BalanceAdjustment, AmountMinor: 100, BalanceUID: "x"}).Effects(); len(eff) != 0 {
		t.Errorf("adjustment effects: %v", eff)
	}
}
