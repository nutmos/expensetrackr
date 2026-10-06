package expense

import (
	"encoding/json"
	"errors"
	"testing"
)

func patchOf(t *testing.T, s string) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func base() CreateInput {
	return Expense{Amount: "120.50", Currency: "THB", Account: "KBank debit",
		SpentAt: "2026-10-05T09:00:00+07:00", Note: "lunch"}.Input()
}

func TestApplyPatchOnlyChangesGivenFields(t *testing.T) {
	in := base()
	if err := in.ApplyPatch(patchOf(t, `{"note":"dinner","account":"Cash"}`)); err != nil {
		t.Fatal(err)
	}
	e, err := in.Validate()
	if err != nil {
		t.Fatal(err)
	}
	if e.Note != "dinner" || e.Account != "Cash" || e.Amount != "120.50" || e.Currency != "THB" || e.SpentAt != "2026-10-05T09:00:00+07:00" {
		t.Errorf("unexpected result: %+v", e)
	}
}

func TestApplyPatchCurrencyRescale(t *testing.T) {
	// THB 120.50 -> KWD (3 decimals): same decimal value, rescaled minor units.
	in := base()
	_ = in.ApplyPatch(patchOf(t, `{"currency":"KWD"}`))
	e, err := in.Validate()
	if err != nil || e.Amount != "120.500" || e.AmountMinor != 120500 {
		t.Errorf("KWD rescale: %+v, %v", e, err)
	}
	// THB 120.50 -> JPY (0 decimals): cannot be represented, so it is rejected.
	in = base()
	_ = in.ApplyPatch(patchOf(t, `{"currency":"JPY"}`))
	var ve *ValidationError
	if _, err := in.Validate(); !errors.As(err, &ve) || ve.Fields["amount"] == "" {
		t.Errorf("JPY with fractional amount should fail on amount, got %v", err)
	}
}

func TestApplyPatchErrors(t *testing.T) {
	var re *RequestError
	var ve *ValidationError
	in := base()
	if err := in.ApplyPatch(patchOf(t, `{}`)); !errors.As(err, &re) {
		t.Errorf("empty patch: %v", err)
	}
	if err := in.ApplyPatch(patchOf(t, `{"tip":"1"}`)); !errors.As(err, &re) {
		t.Errorf("unknown field: %v", err)
	}
	if err := in.ApplyPatch(patchOf(t, `{"currency":123}`)); !errors.As(err, &re) {
		t.Errorf("wrong type: %v", err)
	}
	if err := in.ApplyPatch(patchOf(t, `{"amount":null,"account":null}`)); !errors.As(err, &ve) || len(ve.Fields) != 2 {
		t.Errorf("nulls: %v", err)
	}
	in = base()
	if err := in.ApplyPatch(patchOf(t, `{"note":null}`)); err != nil || in.Note != "" {
		t.Errorf("note null should clear note: %v %q", err, in.Note)
	}
}
