package balance

import (
	"math/big"
	"sort"

	"github.com/nutmos/expensetrackr/pkg/money"
)

// Totals aggregates balances of one currency.
type Totals struct {
	Currency        string `json:"currency"`
	Assets          string `json:"assets"`           // sum of balances (asset types)
	Liabilities     string `json:"liabilities"`      // sum of debts (liability types)
	Net             string `json:"net"`              // assets - liabilities
	CreditLimit     string `json:"credit_limit"`     // sum of limits (liability types)
	AvailableCredit string `json:"available_credit"` // credit_limit - liabilities
}

// currencySum accumulates one currency's amounts exactly.
type currencySum struct {
	scale               int
	assets, debt, limit big.Int
}

func (a *currencySum) add(b Balance) {
	addTo(&a.assets, b.BalanceMinor)
	addTo(&a.debt, b.DebtMinor)
	addTo(&a.limit, b.LimitMinor)
}

func (a *currencySum) totals(currency string) Totals {
	f := func(v *big.Int) string { return money.FormatBigAmount(v, a.scale) }
	return Totals{
		Currency:        currency,
		Assets:          f(&a.assets),
		Liabilities:     f(&a.debt),
		Net:             f(new(big.Int).Sub(&a.assets, &a.debt)),
		CreditLimit:     f(&a.limit),
		AvailableCredit: f(new(big.Int).Sub(&a.limit, &a.debt)),
	}
}

// addTo adds *v to sum; nil adds nothing.
func addTo(sum *big.Int, v *int64) {
	if v != nil {
		sum.Add(sum, big.NewInt(*v))
	}
}

// ComputeTotals sums per currency, exactly in minor units. Currencies are
// returned in alphabetical order. No exchange-rate conversion is done. Sums
// use arbitrary precision, so they are exact even past the int64 range of a
// single amount.
func ComputeTotals(items []Balance) []Totals {
	sums := map[string]*currencySum{}
	for _, b := range items {
		a := sums[b.Currency]
		if a == nil {
			a = &currencySum{scale: b.Scale}
			sums[b.Currency] = a
		}
		a.add(b)
	}
	out := make([]Totals, 0, len(sums))
	for cur, a := range sums {
		out = append(out, a.totals(cur))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Currency < out[j].Currency })
	return out
}
