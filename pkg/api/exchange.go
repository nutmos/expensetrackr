package api

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/nutmos/expensetrackr/pkg/balance"
	"github.com/nutmos/expensetrackr/pkg/fx"
	"github.com/nutmos/expensetrackr/pkg/money"
	"github.com/nutmos/expensetrackr/pkg/transaction"
	"github.com/nutmos/expensetrackr/pkg/validate"

	"github.com/gin-gonic/gin"
)

var errNoFX = fmt.Errorf("%w: no exchange-rate service configured", fx.ErrUnavailable)

func fxMessage(err error) string {
	if errors.Is(err, fx.ErrUnsupported) {
		return "Frankfurter has no exchange rate for this currency pair; provide to_amount (the amount received) manually"
	}
	log.Printf("exchange rate lookup failed: %v", err)
	return "the exchange-rate service (Frankfurter) is unavailable; try again later or provide to_amount (the amount received) manually"
}

const fxMemoKey = "fx_memo"

type fxMemoEntry struct {
	q   fx.Quote
	err error
}

// rate looks up a Frankfurter rate (cached).
func (s *Server) rate(c *gin.Context, from, to, date string, loc *time.Location) (fx.Quote, error) {
	if s.FX == nil {
		return fx.Quote{}, errNoFX
	}
	// A bulk request memoizes lookups (including failures) so an upstream
	// outage costs one attempt per currency pair and date, not one per item.
	if v, ok := c.Get(fxMemoKey); ok {
		memo := v.(map[string]fxMemoEntry)
		key := from + "/" + to + "/" + date
		if m, ok := memo[key]; ok {
			return m.q, m.err
		}
		q, err := s.FX.Rate(c.Request.Context(), from, to, date, loc)
		memo[key] = fxMemoEntry{q, err}
		return q, err
	}
	return s.FX.Rate(c.Request.Context(), from, to, date, loc)
}

// getExchangeRate handles GET /api/exchange-rates?from=USD&to=THB&date=YYYY-MM-DD
// (date optional: latest). Used by the web form to preview a conversion.
func (s *Server) getExchangeRate(c *gin.Context) {
	fields := map[string]string{}
	from := strings.ToUpper(strings.TrimSpace(c.Query("from")))
	to := strings.ToUpper(strings.TrimSpace(c.Query("to")))
	for name, v := range map[string]string{"from": from, "to": to} {
		if v == "" {
			fields[name] = "is required"
		} else if _, ok := money.MinorUnits(v); !ok {
			fields[name] = "is not a recognised ISO 4217 currency code"
		}
	}
	date := strings.TrimSpace(c.Query("date"))
	if date != "" {
		if _, err := time.Parse(time.DateOnly, date); err != nil {
			fields["date"] = "must be a date like 2026-10-12 (YYYY-MM-DD)"
		}
	}
	if len(fields) > 0 {
		c.JSON(http.StatusBadRequest, errorBody{Error: "invalid query parameters", Fields: fields})
		return
	}
	if from == to {
		d := date
		if d == "" {
			d = time.Now().Format(time.DateOnly)
		}
		c.JSON(http.StatusOK, fx.Quote{From: from, To: to, Rate: "1", Date: d, Source: "same_currency"})
		return
	}
	q, err := s.rate(c, from, to, date, time.Local)
	if err != nil {
		writeInputError(c, err)
		return
	}
	c.JSON(http.StatusOK, q)
}

// convertTransfer fills the cross-currency fields of a transfer e whose
// balances src and dst have been resolved. in carries the client's
// to_amount / to_currency; prev (PUT only) is the stored transaction, whose
// conversion is kept when nothing that affects it changed and to_amount is
// omitted. Stored rates are never refetched for reversal.
func (s *Server) convertTransfer(c *gin.Context, e *transaction.Transaction, in transaction.CreateInput, src, dst balance.Balance, prev *transaction.Transaction) error {
	e.ToAmount, e.ToAmountMinor, e.ToCurrency, e.ExchangeRate, e.RateSource, e.RateDate = nil, nil, nil, nil, nil, nil
	if e.Type != transaction.Transfer || dst.UID == "" {
		return nil
	}
	rawTo := strings.TrimSpace(string(in.ToAmount))
	if tc := strings.ToUpper(strings.TrimSpace(in.ToCurrency)); tc != "" && tc != dst.Currency {
		return &validate.ValidationError{Fields: map[string]string{"to_currency": fmt.Sprintf("must be %s, the currency of the destination balance %q", dst.Currency, dst.Name)}}
	}
	if src.Currency != e.Currency {
		// Same message the store gives; checked first so no rate is fetched.
		return &validate.ValidationError{Fields: map[string]string{"balance_uid": fmt.Sprintf(
			"balance %q is in %s but the transaction is in %s; the currencies must match", src.Name, src.Currency, e.Currency)}}
	}
	toScale, _ := money.MinorUnits(dst.Currency)
	fromScale, _ := money.MinorUnits(e.Currency)
	if dst.Currency == e.Currency {
		if rawTo != "" {
			m, err := money.ParseAmount(rawTo, toScale)
			if err != nil {
				return &validate.ValidationError{Fields: map[string]string{"to_amount": err.Error()}}
			}
			if m != e.AmountMinor {
				return &validate.ValidationError{Fields: map[string]string{"to_amount": "must equal amount (or be omitted) when both balances use the same currency"}}
			}
		}
		return nil
	}
	cur := dst.Currency
	e.ToCurrency = &cur
	if rawTo != "" {
		m, err := money.ParseAmount(rawTo, toScale)
		if err != nil {
			return &validate.ValidationError{Fields: map[string]string{"to_amount": err.Error()}}
		}
		amt, rate, src := money.FormatAmount(m, toScale), fx.ManualRate(e.AmountMinor, fromScale, m, toScale), "manual"
		e.ToAmountMinor, e.ToAmount, e.ExchangeRate, e.RateSource = &m, &amt, &rate, &src
		return nil
	}
	if prev != nil && prev.ToCurrency != nil && prev.ToAmountMinor != nil && *prev.ToCurrency == cur &&
		prev.Type == e.Type && prev.Currency == e.Currency && prev.AmountMinor == e.AmountMinor &&
		prev.BalanceUID == e.BalanceUID && prev.ToBalanceUID != nil && *prev.ToBalanceUID == *e.ToBalanceUID &&
		localDate(prev.SpentAt) == localDate(e.SpentAt) {
		e.ToAmountMinor, e.ToAmount, e.ExchangeRate, e.RateSource, e.RateDate = prev.ToAmountMinor, prev.ToAmount, prev.ExchangeRate, prev.RateSource, prev.RateDate
		return nil
	}
	q, err := s.rate(c, e.Currency, cur, localDate(e.SpentAt), e.SpentTime.Location())
	if err != nil {
		return err
	}
	m, err := fx.Convert(e.AmountMinor, fromScale, q.Rate, toScale)
	if err != nil {
		return &validate.ValidationError{Fields: map[string]string{"amount": err.Error()}}
	}
	amt, rate, srcName, date := money.FormatAmount(m, toScale), q.Rate, fx.Source, q.Date
	e.ToAmountMinor, e.ToAmount, e.ExchangeRate, e.RateSource, e.RateDate = &m, &amt, &rate, &srcName, &date
	return nil
}

// localDate is the YYYY-MM-DD part of an RFC 3339 spent_at, i.e. the date
// in the offset the user entered.
func localDate(spentAt string) string {
	if len(spentAt) >= 10 {
		return spentAt[:10]
	}
	return spentAt
}
