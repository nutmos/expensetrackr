// Package balance models account balances: payment accounts and other assets
// (which have a balance) and credit cards and other liabilities (which have a
// debt and a limit). Amounts are integer minor units, like expenses.
package balance

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strings"

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
	CreatedAt      string          `json:"created_at"`
	UpdatedAt      *string         `json:"updated_at"`
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
	b.Balance, b.BalanceMinor = nil, nil
	b.Debt, b.DebtMinor, b.Limit, b.LimitMinor = nil, nil, nil, nil
	b.Available, b.AvailableMinor, b.OverLimit = nil, nil, nil
	format := func(v int64) *string { s := money.FormatAmount(v, scale); return &s }
	if bal != nil {
		v := *bal
		b.BalanceMinor, b.Balance = &v, format(v)
	}
	if debt != nil {
		v := *debt
		b.DebtMinor, b.Debt = &v, format(v)
	}
	if limit != nil {
		v := *limit
		b.LimitMinor, b.Limit = &v, format(v)
	}
	if debt != nil && limit != nil {
		over := *debt > *limit
		b.OverLimit = &over
		// ApplyValueDelta keeps limit - debt within int64; a value that still
		// doesn't fit is left out rather than wrapped around.
		if avail, ok := subChecked(*limit, *debt); ok {
			b.AvailableMinor, b.Available = &avail, format(avail)
		}
	}
}

// ErrOverflow is returned by ApplyValueDelta when the result does not fit.
var ErrOverflow = errors.New("amount out of range")

// ApplyValueDelta changes the balance's value by delta minor units (same
// currency and scale as the balance). Value is the balance of an asset and
// minus the debt of a liability, so a positive delta raises an asset's
// balance or lowers a liability's debt (possibly below zero: in credit). The
// derived fields (available, over_limit) are recomputed.
//
// It returns ErrOverflow, leaving b unchanged, when the new balance or debt,
// or a liability's available amount (limit - debt), would not fit in int64.
func (b *Balance) ApplyValueDelta(delta int64) error {
	if b.Type.IsAsset() && b.BalanceMinor != nil {
		v, ok := addChecked(*b.BalanceMinor, delta)
		if !ok {
			return ErrOverflow
		}
		b.SetAmounts(b.Scale, &v, nil, nil)
	} else if b.Type.IsLiability() && b.DebtMinor != nil && b.LimitMinor != nil {
		if delta == math.MinInt64 {
			return ErrOverflow
		}
		v, ok := addChecked(*b.DebtMinor, -delta)
		if !ok {
			return ErrOverflow
		}
		limit := *b.LimitMinor
		if _, ok := subChecked(limit, v); !ok {
			return ErrOverflow
		}
		b.SetAmounts(b.Scale, nil, &v, &limit)
	} else {
		return fmt.Errorf("balance %q has no amount for type %q", b.UID, b.Type)
	}
	return nil
}

func addChecked(a, b int64) (int64, bool) {
	c := a + b
	if (b > 0 && c < a) || (b < 0 && c > a) {
		return 0, false
	}
	return c, true
}

// subChecked returns a - b and whether it fits in int64.
func subChecked(a, b int64) (int64, bool) {
	c := a - b
	if (b > 0 && c > a) || (b < 0 && c < a) {
		return 0, false
	}
	return c, true
}

// Totals aggregates balances of one currency.
type Totals struct {
	Currency        string `json:"currency"`
	Assets          string `json:"assets"`           // sum of balances (asset types)
	Liabilities     string `json:"liabilities"`      // sum of debts (liability types)
	Net             string `json:"net"`              // assets - liabilities
	CreditLimit     string `json:"credit_limit"`     // sum of limits (liability types)
	AvailableCredit string `json:"available_credit"` // credit_limit - liabilities
}

// ComputeTotals sums per currency, exactly in minor units. Currencies are
// returned in alphabetical order. No exchange-rate conversion is done. Sums
// use arbitrary precision, so they are exact even past the int64 range of a
// single amount.
func ComputeTotals(items []Balance) []Totals {
	type acc struct {
		scale               int
		assets, debt, limit big.Int
	}
	sums := map[string]*acc{}
	for _, b := range items {
		a := sums[b.Currency]
		if a == nil {
			a = &acc{scale: b.Scale}
			sums[b.Currency] = a
		}
		if b.BalanceMinor != nil {
			a.assets.Add(&a.assets, big.NewInt(*b.BalanceMinor))
		}
		if b.DebtMinor != nil {
			a.debt.Add(&a.debt, big.NewInt(*b.DebtMinor))
		}
		if b.LimitMinor != nil {
			a.limit.Add(&a.limit, big.NewInt(*b.LimitMinor))
		}
	}
	out := make([]Totals, 0, len(sums))
	for cur, a := range sums {
		f := func(v *big.Int) string { return formatBigAmount(v, a.scale) }
		out = append(out, Totals{
			Currency: cur, Assets: f(&a.assets), Liabilities: f(&a.debt), Net: f(new(big.Int).Sub(&a.assets, &a.debt)),
			CreditLimit: f(&a.limit), AvailableCredit: f(new(big.Int).Sub(&a.limit, &a.debt)),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Currency < out[j].Currency })
	return out
}

// formatBigAmount is money.FormatAmount for an arbitrary-precision amount:
// minor units with exponent decimal places, e.g. 12345 at 2 -> "123.45".
func formatBigAmount(minor *big.Int, exponent int) string {
	if minor.IsInt64() {
		return money.FormatAmount(minor.Int64(), exponent)
	}
	digits := new(big.Int).Abs(minor).String()
	if exponent > 0 {
		if len(digits) <= exponent {
			digits = strings.Repeat("0", exponent-len(digits)+1) + digits
		}
		digits = digits[:len(digits)-exponent] + "." + digits[len(digits)-exponent:]
	}
	if minor.Sign() < 0 {
		digits = "-" + digits
	}
	return digits
}
