package category

import (
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
