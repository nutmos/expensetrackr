// Package category models transaction categories. A category is either an
// expense category or an income category; expense transactions may only use
// expense categories and income transactions income categories.
package category

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/nutmos/expensetrackr/pkg/validate"
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

const (
	MaxNameLen        = 100
	MaxDescriptionLen = 1000
)

// Category is a stored category record.
type Category struct {
	ID          int64   `json:"-"`   // internal row id; the API identifies categories by UID
	UID         string  `json:"uid"` // server-assigned UUID v4; immutable
	Name        string  `json:"name"`
	Type        Type    `json:"type"`
	Description string  `json:"description"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   *string `json:"updated_at"`
}

// Input is the payload for POST /api/categories and PUT /api/categories/:uid.
type Input struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`

	// IgnoredUID lets a previous response be round-tripped; never used.
	IgnoredUID string `json:"uid,omitempty"`
}

// Validate checks the input and returns a normalized Category (no UID/ID/times).
func (in Input) Validate() (Category, error) {
	fields := map[string]string{}
	var c Category

	name := strings.TrimSpace(in.Name)
	switch {
	case name == "":
		fields["name"] = "is required"
	case utf8.RuneCountInString(name) > MaxNameLen:
		fields["name"] = fmt.Sprintf("must be at most %d characters", MaxNameLen)
	}
	c.Name = name

	if t, ok := ParseType(in.Type); ok {
		c.Type = t
	} else if strings.TrimSpace(in.Type) == "" {
		fields["type"] = "is required (expense or income)"
	} else {
		fields["type"] = "must be one of: expense, income"
	}

	desc := strings.TrimSpace(in.Description)
	if utf8.RuneCountInString(desc) > MaxDescriptionLen {
		fields["description"] = fmt.Sprintf("must be at most %d characters", MaxDescriptionLen)
	}
	c.Description = desc

	if len(fields) > 0 {
		return Category{}, &validate.ValidationError{Fields: fields}
	}
	return c, nil
}

// Input returns the editable fields of c, e.g. to build a PUT body from a stored record.
func (c Category) Input() Input {
	return Input{Name: c.Name, Type: string(c.Type), Description: c.Description}
}
