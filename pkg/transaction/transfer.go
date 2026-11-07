package transaction

import (
	"fmt"
	"strings"
	"time"

	"github.com/nutmos/expensetrackr/pkg/balance"
	"github.com/nutmos/expensetrackr/pkg/fx"
	"github.com/nutmos/expensetrackr/pkg/money"
)

// RateFunc looks up an exchange rate: Quote.Rate units of to per 1 from, for
// date (YYYY-MM-DD) as seen in loc. The API passes a Frankfurter lookup
// (cached, memoized per bulk request); tests pass a stub.
type RateFunc func(from, to, date string, loc *time.Location) (fx.Quote, error)

// ConvertTransfer fills the cross-currency fields of a transfer e whose
// balances src and dst have been resolved (see docs/exchange-rates.md):
//
//   - not a transfer, or no destination: the fields are cleared;
//   - to_currency, if sent, must be dst's currency, and src must be in the
//     transaction currency (checked before any rate lookup);
//   - same currency: to_amount, if sent, must equal amount; nothing is stored;
//   - to_amount sent: a manual rate (to_amount / amount);
//   - PUT (prev set) with nothing that affects the conversion changed and no
//     to_amount: the stored conversion is kept (rates are never refetched);
//   - otherwise rate is called for the spent_at date and the amount
//     converted, rounded half up to dst's minor units.
//
// in carries the client's to_amount / to_currency. Errors are a
// *ValidationError, or rate's error unchanged (fx.ErrUnavailable, ...).
func (e *Transaction) ConvertTransfer(in CreateInput, src, dst balance.Balance, prev *Transaction, rate RateFunc) error {
	e.ToAmount, e.ToAmountMinor, e.ToCurrency, e.ExchangeRate, e.RateSource, e.RateDate = nil, nil, nil, nil, nil, nil
	if e.Type != Transfer || dst.UID == "" {
		return nil
	}
	rawTo := strings.TrimSpace(string(in.ToAmount))
	if tc := strings.ToUpper(strings.TrimSpace(in.ToCurrency)); tc != "" && tc != dst.Currency {
		return fieldError("to_currency", fmt.Sprintf("must be %s, the currency of the destination balance %q", dst.Currency, dst.Name))
	}
	if src.Currency != e.Currency {
		// Same message the store gives; checked first so no rate is fetched.
		return fieldError("balance_uid", fmt.Sprintf(
			"balance %q is in %s but the transaction is in %s; the currencies must match", src.Name, src.Currency, e.Currency))
	}
	toScale, _ := money.MinorUnits(dst.Currency)
	fromScale, _ := money.MinorUnits(e.Currency)
	if dst.Currency == e.Currency {
		if rawTo != "" {
			m, err := money.ParseAmount(rawTo, toScale)
			if err != nil {
				return fieldError("to_amount", err.Error())
			}
			if m != e.AmountMinor {
				return fieldError("to_amount", "must equal amount (or be omitted) when both balances use the same currency")
			}
		}
		return nil
	}
	cur := dst.Currency
	e.ToCurrency = &cur
	if rawTo != "" {
		m, err := money.ParseAmount(rawTo, toScale)
		if err != nil {
			return fieldError("to_amount", err.Error())
		}
		amt, r, source := money.FormatAmount(m, toScale), fx.ManualRate(e.AmountMinor, fromScale, m, toScale), "manual"
		e.ToAmountMinor, e.ToAmount, e.ExchangeRate, e.RateSource = &m, &amt, &r, &source
		return nil
	}
	if e.conversionUnchanged(prev) {
		e.ToAmountMinor, e.ToAmount, e.ExchangeRate, e.RateSource, e.RateDate = prev.ToAmountMinor, prev.ToAmount, prev.ExchangeRate, prev.RateSource, prev.RateDate
		return nil
	}
	q, err := rate(e.Currency, cur, LocalDate(e.SpentAt), e.SpentTime.Location())
	if err != nil {
		return err
	}
	m, err := fx.Convert(e.AmountMinor, fromScale, q.Rate, toScale)
	if err != nil {
		return fieldError("amount", err.Error())
	}
	amt, r, source, date := money.FormatAmount(m, toScale), q.Rate, fx.Source, q.Date
	e.ToAmountMinor, e.ToAmount, e.ExchangeRate, e.RateSource, e.RateDate = &m, &amt, &r, &source, &date
	return nil
}

// conversionUnchanged reports whether prev (the stored transaction on PUT)
// has a conversion into e.ToCurrency and nothing that affects it changed:
// type, currency, amount, both balances and the spent_at date.
func (e *Transaction) conversionUnchanged(prev *Transaction) bool {
	return prev != nil && prev.ToCurrency != nil && prev.ToAmountMinor != nil && *prev.ToCurrency == *e.ToCurrency &&
		prev.Type == e.Type && prev.Currency == e.Currency && prev.AmountMinor == e.AmountMinor &&
		prev.BalanceUID == e.BalanceUID && prev.ToBalanceUID != nil && e.ToBalanceUID != nil && *prev.ToBalanceUID == *e.ToBalanceUID &&
		LocalDate(prev.SpentAt) == LocalDate(e.SpentAt)
}

// LocalDate is the YYYY-MM-DD part of an RFC 3339 spent_at, i.e. the date in
// the offset the user entered.
func LocalDate(spentAt string) string {
	if len(spentAt) >= 10 {
		return spentAt[:10]
	}
	return spentAt
}
