package api

import (
	"context"
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

// DefaultBulkFXTimeout bounds the time one bulk request spends fetching
// exchange rates (Server.BulkFXTimeout overrides it). Lookups still pending
// when it runs out fail as exchange_rate_unavailable. The HTTP server's
// WriteTimeout must stay well above it (cmd/server).
const DefaultBulkFXTimeout = 30 * time.Second

type fxMemoEntry struct {
	q   fx.Quote
	err error
}

// fxMemo is the per-request rate memo of a bulk import (issue #83).
type fxMemo struct {
	// ctx bounds the Frankfurter fetches of the whole request
	// (DefaultBulkFXTimeout); cache lookups use the request context.
	ctx     context.Context
	entries map[string]fxMemoEntry
	// down is the first "service down" error (ErrUnavailable or
	// ErrBadUpstream). After it, uncached lookups fail at once with it
	// instead of trying Frankfurter again for every new pair or date.
	down error
}

func newFXMemo(ctx context.Context) *fxMemo {
	return &fxMemo{ctx: ctx, entries: map[string]fxMemoEntry{}}
}

// rate looks up a Frankfurter rate (cached).
func (s *Server) rate(c *gin.Context, from, to, date string, loc *time.Location) (fx.Quote, error) {
	if s.FX == nil {
		return fx.Quote{}, errNoFX
	}
	// A bulk request memoizes lookups (including failures), and once
	// Frankfurter is found to be down it stops calling it: an outage costs
	// one attempt per request, not one per currency pair and date. Rates
	// already in the cache keep working.
	if v, ok := c.Get(fxMemoKey); ok {
		memo := v.(*fxMemo)
		key := from + "/" + to + "/" + date
		if m, ok := memo.entries[key]; ok {
			return m.q, m.err
		}
		var q fx.Quote
		var err error
		if memo.down != nil {
			var cached bool
			if q, cached = s.FX.Cached(c.Request.Context(), from, to, date, loc); !cached {
				err = memo.down
			}
		} else {
			q, err = s.FX.Rate(memo.ctx, from, to, date, loc)
			if errors.Is(err, fx.ErrUnavailable) || errors.Is(err, fx.ErrBadUpstream) {
				memo.down = err
			}
		}
		memo.entries[key] = fxMemoEntry{q, err}
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

// convertTransfer fills the cross-currency fields of a transfer
// (transaction.ConvertTransfer), looking rates up through s.rate so they are
// cached and, in a bulk request, memoized and time-bounded.
func (s *Server) convertTransfer(c *gin.Context, e *transaction.Transaction, in transaction.CreateInput, src, dst balance.Balance, prev *transaction.Transaction) error {
	return e.ConvertTransfer(in, src, dst, prev, func(from, to, date string, loc *time.Location) (fx.Quote, error) {
		return s.rate(c, from, to, date, loc)
	})
}
