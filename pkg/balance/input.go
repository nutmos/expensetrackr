package balance

import (
	"strings"

	"github.com/nutmos/expensetrackr/pkg/common_type"
	"github.com/nutmos/expensetrackr/pkg/money"
)

// Input is the payload for POST and PUT /api/balances. An amount that is absent, null
// or "" counts as not provided.
// UID is accepted so clients can round-trip a previous response; it is
// ignored (the server assigns and never changes it).
type Input struct {
	UID         common_type.UID     `json:"uid,omitempty"` // ignored; server-assigned
	Name        string              `json:"name"`
	Type        string              `json:"type"`
	Currency    string              `json:"currency"`
	Description string              `json:"description"`
	Balance     *money.DecimalInput `json:"balance"`
	Debt        *money.DecimalInput `json:"debt"`
	Limit       *money.DecimalInput `json:"limit"`

	// Version is the optimistic-locking version for PUT (alternative to the
	// If-Match header). Ignored on create.
	Version *int64 `json:"version,omitempty"`
}

// Input returns the editable fields of a stored balance, e.g. to build a PUT
// body from a stored record.
func (b Balance) Input() Input {
	return Input{
		Name: b.Name, Type: string(b.Type), Currency: b.Currency, Description: b.Description,
		Balance: decimalInput(b.Balance), Debt: decimalInput(b.Debt), Limit: decimalInput(b.Limit),
	}
}

func decimalInput(s *string) *money.DecimalInput {
	if s == nil {
		return nil
	}
	d := money.DecimalInput(*s)
	return &d
}

// provided reports whether an amount was sent (not absent, null or blank).
func provided(d *money.DecimalInput) bool {
	return d != nil && strings.TrimSpace(string(*d)) != ""
}
