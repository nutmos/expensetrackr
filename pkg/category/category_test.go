package category

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/nutmos/expensetrackr/pkg/validate"
)

func TestValidate(t *testing.T) {
	c, err := Input{Name: "  Food ", Type: "EXPENSE", Description: " x "}.Validate()
	if err != nil || c.Name != "Food" || c.Type != Expense || c.Description != "x" {
		t.Fatalf("got %+v %v", c, err)
	}
	_, err = Input{Type: "refund"}.Validate()
	var ve *validate.ValidationError
	if !errors.As(err, &ve) || ve.Fields["name"] == "" || ve.Fields["type"] == "" {
		t.Fatalf("want name+type errors, got %v", err)
	}
}

func TestApplyPatch(t *testing.T) {
	in := Category{Name: "Food", Type: Expense, Description: "d"}.Input()
	var p map[string]json.RawMessage
	_ = json.Unmarshal([]byte(`{"description":null,"uid":"x","name":"Meals"}`), &p)
	if err := in.ApplyPatch(p); err != nil || in.Description != "" || in.Name != "Meals" {
		t.Fatalf("%+v %v", in, err)
	}
	_ = json.Unmarshal([]byte(`{"colour":"red"}`), &p)
	var re *validate.RequestError
	if err := in.ApplyPatch(p); !errors.As(err, &re) {
		t.Fatalf("unknown field: %v", err)
	}
	p = map[string]json.RawMessage{"type": json.RawMessage("null")}
	var ve *validate.ValidationError
	if err := in.ApplyPatch(p); !errors.As(err, &ve) {
		t.Fatalf("null type: %v", err)
	}
}
