package expense

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// patchFields lists the fields a PATCH request may change.
var patchFields = map[string]bool{
	"amount": true, "currency": true, "balance_uid": true, "spent_at": true, "note": true,
}

// Input returns the editable fields of a stored expense as a CreateInput, so
// that a partial update can be applied on top and re-validated as a whole.
// Account is not editable (it is a denormalized snapshot of the balance name).
func (e Expense) Input() CreateInput {
	return CreateInput{
		Amount:     DecimalInput(e.Amount),
		Currency:   e.Currency,
		BalanceUID: e.BalanceUID,
		SpentAt:    e.SpentAt,
		Note:       e.Note,
	}
}

// ApplyPatch overlays the fields present in a JSON-object patch (decoded as raw
// messages) onto in. Fields that are absent are left unchanged. "note": null
// clears the note; null for any other field is a validation error. A legacy
// "account" field is rejected (payment account is now balance_uid). The caller
// must run Validate on the result.
func (in *CreateInput) ApplyPatch(patch map[string]json.RawMessage) error {
	if len(patch) == 0 {
		return &RequestError{Msg: "patch must contain at least one of: amount, currency, balance_uid, spent_at, note"}
	}
	var unknown []string
	for k := range patch {
		if k == "account" {
			return &RequestError{Msg: `unknown field "account" (use "balance_uid" to set the payment account)`}
		}
		if !patchFields[k] {
			unknown = append(unknown, fmt.Sprintf("%q", k))
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return &RequestError{Msg: "unknown field " + strings.Join(unknown, ", ")}
	}

	nullFields := map[string]string{}
	str := func(name string, dst *string) error {
		raw, ok := patch[name]
		if !ok {
			return nil
		}
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			if name == "note" {
				*dst = ""
			} else {
				nullFields[name] = "cannot be null"
			}
			return nil
		}
		if err := json.Unmarshal(raw, dst); err != nil {
			return &RequestError{Msg: fmt.Sprintf("field %q must be a string", name)}
		}
		return nil
	}
	for _, f := range []struct {
		name string
		dst  *string
	}{{"currency", &in.Currency}, {"balance_uid", &in.BalanceUID}, {"spent_at", &in.SpentAt}, {"note", &in.Note}} {
		if err := str(f.name, f.dst); err != nil {
			return err
		}
	}
	if raw, ok := patch["amount"]; ok {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			nullFields["amount"] = "cannot be null"
		} else if err := json.Unmarshal(raw, &in.Amount); err != nil {
			return &RequestError{Msg: `field "amount" must be a decimal string or number`}
		}
	}
	if len(nullFields) > 0 {
		return &ValidationError{Fields: nullFields}
	}
	return nil
}
