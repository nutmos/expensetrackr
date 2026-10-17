package transaction

import (
	"encoding/json"

	"github.com/nutmos/expensetrackr/pkg/fx"
	"github.com/nutmos/expensetrackr/pkg/money"
)

// ConversionRate is one direction of the conversion a cross-currency
// transfer actually made: Rate units of To per 1 From. It has the same
// from/to/rate shape as GET /api/exchange-rates.
type ConversionRate struct {
	From string `json:"from"` // ISO 4217
	To   string `json:"to"`   // ISO 4217
	Rate string `json:"rate"` // decimal string, up to fx.RateSigDigits significant digits
}

// ComputeConversionRates returns both directions of the effective rate of a
// cross-currency transfer, derived from its own amounts: first
// currency -> to_currency (to_amount / amount), then to_currency -> currency
// (amount / to_amount). Exact rational arithmetic on the minor units,
// formatted like exchange_rate (fx.FormatRate, half-up).
//
// It returns nil (JSON null) for every other type, for same-currency
// transfers (which store no to_amount; the rate is trivially 1) and when any
// amount is zero or missing, so no division by zero can happen. The result is
// never stored; MarshalJSON computes it whenever a transaction is encoded.
func (e Transaction) ComputeConversionRates() []ConversionRate {
	if e.Type != Transfer || e.ToCurrency == nil || e.ToAmountMinor == nil {
		return nil
	}
	from, to := e.Currency, *e.ToCurrency
	if from == "" || to == "" || from == to || e.AmountMinor <= 0 || *e.ToAmountMinor <= 0 {
		return nil
	}
	fromScale, ok1 := money.MinorUnits(from)
	toScale, ok2 := money.MinorUnits(to)
	if !ok1 || !ok2 {
		return nil
	}
	return []ConversionRate{
		{From: from, To: to, Rate: fx.ManualRate(e.AmountMinor, fromScale, *e.ToAmountMinor, toScale)},
		{From: to, To: from, Rate: fx.ManualRate(*e.ToAmountMinor, toScale, e.AmountMinor, fromScale)},
	}
}

// MarshalJSON encodes the transaction with conversion_rates computed on the
// fly (any value already in the ConversionRates field is ignored), so every
// response that carries a transaction (single, list, create/update, bulk,
// 409 conflict bodies) includes it.
func (e Transaction) MarshalJSON() ([]byte, error) {
	type wire Transaction // same fields, no methods (avoids recursion)
	w := wire(e)
	w.ConversionRates = e.ComputeConversionRates()
	return json.Marshal(w)
}
