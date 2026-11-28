// Package balance models account balances: payment accounts and other assets
// (which have a balance) and credit cards and other liabilities (which have a
// debt and a limit). Amounts are integer minor units, like expenses.
//
// Files: type.go (types and kinds), balance.go (the record and its derived
// fields), delta.go (moving a balance by a transaction), totals.go (sums per
// currency), input.go and validate.go (the API payload and its checks).
package balance

import (
	"github.com/nutmos/expensetrackr/pkg/common_type"
	"github.com/nutmos/expensetrackr/pkg/money"
)

const (
	MaxNameLen        = 100 // same as an expense's payment account, so names can match later
	MaxDescriptionLen = 1000
)

// Balance is a stored balance record. Fields that do not apply to the type are
// null in JSON: assets have balance; liabilities have debt, limit, available
// and over_limit.
type Balance struct {
	ID             int64           `json:"-"`   // internal row id; the API identifies balances by UID
	UID            common_type.UID `json:"uid"` // server-assigned UUID v4; immutable
	Name           string          `json:"name"`
	Type           Type            `json:"type"`
	Kind           Kind            `json:"kind"`     // "asset" or "liability" (derived from type)
	Currency       string          `json:"currency"` // base currency, ISO 4217
	Description    string          `json:"description"`
	Balance        *string         `json:"balance"` // decimal string; may be negative (overdraft)
	BalanceMinor   *int64          `json:"balance_minor"`
	Debt           *string         `json:"debt"` // may be negative (overpaid / in credit)
	DebtMinor      *int64          `json:"debt_minor"`
	Limit          *string         `json:"limit"` // >= 0
	LimitMinor     *int64          `json:"limit_minor"`
	Available      *string         `json:"available"` // limit - debt; negative when over the limit
	AvailableMinor *int64          `json:"available_minor"`
	OverLimit      *bool           `json:"over_limit"` // debt > limit
	// Hidden balances are left out of GET /api/balances unless asked for
	// (?include_hidden=true) and out of the web page's pickers; their
	// transactions are kept and still count. See docs/balances.md §5.
	Hidden    bool    `json:"hidden"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt *string `json:"updated_at"`
	// Version starts at 1 and is incremented on every change, manual (PUT) or
	// automatic (a transaction moving the balance). PUT must
	// send the version they last read (If-Match or "version").
	Version int64 `json:"version"`
	Scale   int   `json:"-"` // minor-unit digits used for the *_minor values
}

// SetAmounts fills the amount fields and the derived ones from minor units.
// Pass nil for amounts that do not apply to the type.
func (b *Balance) SetAmounts(scale int, bal, debt, limit *int64) {
	b.Scale = scale
	b.Kind = b.Type.Kind()
	b.BalanceMinor, b.Balance = b.amount(bal)
	b.DebtMinor, b.Debt = b.amount(debt)
	b.LimitMinor, b.Limit = b.amount(limit)
	b.AvailableMinor, b.Available, b.OverLimit = nil, nil, nil
	if debt != nil && limit != nil {
		over := *debt > *limit
		b.OverLimit = &over
		// ApplyValueDelta keeps limit - debt within int64; a value that still
		// doesn't fit is left out rather than wrapped around.
		if avail, ok := subChecked(*limit, *debt); ok {
			b.AvailableMinor, b.Available = b.amount(&avail)
		}
	}
}

// amount copies v and formats it at b.Scale; nil stays nil.
func (b *Balance) amount(v *int64) (*int64, *string) {
	if v == nil {
		return nil, nil
	}
	n, s := *v, money.FormatAmount(*v, b.Scale)
	return &n, &s
}

// IsPayable reports whether b can pay for a transaction (see Type.IsPayable).
func (b Balance) IsPayable() bool { return b.Type.IsPayable() }

// Filter returns the balances for which keep is true, in order, in a new slice.
func Filter(items []Balance, keep func(Balance) bool) []Balance {
	out := make([]Balance, 0, len(items))
	for _, b := range items {
		if keep(b) {
			out = append(out, b)
		}
	}
	return out
}

// Shown reports whether b is not hidden; use it with Filter.
func Shown(b Balance) bool { return !b.Hidden }
