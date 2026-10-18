// Package category models transaction categories. A category is either an
// expense category or an income category; expense transactions may only use
// expense categories and income transactions income categories.
package category

import (
	"strings"

	"github.com/nutmos/expensetrackr/pkg/common_type"
)

// Type is the kind of category.
type Type string

const (
	Expense Type = "expense"
	Income  Type = "income"
)

// Types lists every type in display order.
var Types = []Type{Expense, Income}

// Valid reports whether t is a known type.
func (t Type) Valid() bool { return t == Expense || t == Income }

// ParseType normalizes s; ok is false for unknown or empty values.
func ParseType(s string) (Type, bool) {
	t := Type(strings.ToLower(strings.TrimSpace(s)))
	return t, t.Valid()
}

// Category is a stored category record.
type Category struct {
	UID         common_type.UID `json:"uid"` // server-assigned UUID v4; immutable
	Name        string          `json:"name"`
	Type        Type            `json:"type"`
	Description string          `json:"description"`
	CreatedAt   string          `json:"created_at"`
	UpdatedAt   *string         `json:"updated_at"`
}

// Input returns the editable fields of c, e.g. to build a PUT body from a stored record.
func (c Category) Input() Input {
	return Input{Name: c.Name, Type: string(c.Type), Description: c.Description}
}
