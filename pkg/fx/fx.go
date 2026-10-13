// Package fx fetches currency exchange rates from Frankfurter
// (https://frankfurter.dev, European Central Bank reference rates) and does
// the exact decimal arithmetic for cross-currency transfers. See
// docs/exchange-rates.md.
package fx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nutmos/expensetrackr/pkg/money"
)

// DefaultBaseURL is the canonical Frankfurter API base (api.frankfurter.app
// redirects here).
const DefaultBaseURL = "https://api.frankfurter.dev/v1"

// Source is the rate_source value for rates fetched from Frankfurter.
const Source = "frankfurter"

// LatestTTL is how long a 'latest' (or recent-date) lookup is cached.
const LatestTTL = 3 * time.Hour

// settledAfter: a requested date this many days in the past maps to a final
// ECB working day, so its lookup is cached forever.
const settledAfter = 7 * 24 * time.Hour

var (
	// ErrUnsupported: Frankfurter has no rate for this currency pair.
	ErrUnsupported = errors.New("exchange rate not available for this currency pair")
	// ErrUnavailable: Frankfurter could not be reached or timed out.
	ErrUnavailable = errors.New("exchange-rate service unavailable")
	// ErrBadUpstream: Frankfurter answered with an error or unexpected body.
	ErrBadUpstream = errors.New("exchange-rate service returned an unexpected response")
)

// Cache stores rates; *store.Store implements it.
type Cache interface {
	FXLookup(ctx context.Context, base, quote, requested string) (rate, rateDate string, fetchedAt time.Time, ok bool, err error)
	FXStore(ctx context.Context, base, quote, requested, rateDate, rate string, fetchedAt time.Time) error
}

// Client looks up rates, using Cache first.
type Client struct {
	BaseURL string       // default DefaultBaseURL
	HTTP    *http.Client // default: 5s timeout, follows redirects
	Cache   Cache        // optional
	Now     func() time.Time
}

// Quote is one exchange rate: Rate units of To per 1 From, valid on Date.
type Quote struct {
	From string `json:"from"`
	To   string `json:"to"`
	Rate string `json:"rate"`
	Date string `json:"date"`
	// Source is "frankfurter".
	Source string `json:"source"`
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Rate returns the rate from -> to for date (YYYY-MM-DD; "" or a date that
// is today or later in loc means the latest rate). Frankfurter answers a
// weekend or holiday with the previous working day; Quote.Date tells which.
func (c *Client) Rate(ctx context.Context, from, to, date string, loc *time.Location) (Quote, error) {
	from, to = strings.ToUpper(from), strings.ToUpper(to)
	if loc == nil {
		loc = time.UTC
	}
	now := c.now()
	requested := "latest"
	if date != "" && date < now.In(loc).Format(time.DateOnly) {
		requested = date
	}
	if c.Cache != nil {
		rate, rd, at, ok, err := c.Cache.FXLookup(ctx, from, to, requested)
		if err == nil && ok && c.fresh(requested, rd, at, now) {
			return Quote{From: from, To: to, Rate: rate, Date: rd, Source: Source}, nil
		}
	}
	q, err := c.fetch(ctx, from, to, requested)
	if err != nil {
		return Quote{}, err
	}
	if c.Cache != nil {
		_ = c.Cache.FXStore(ctx, from, to, requested, q.Date, q.Rate, now) // best effort
	}
	return q, nil
}

// fresh reports whether a cached lookup can be reused: an old requested date
// that resolved to itself or is long settled is final; 'latest' and recent
// dates expire after LatestTTL.
func (c *Client) fresh(requested, rateDate string, at, now time.Time) bool {
	if requested != "latest" {
		if requested == rateDate {
			return true
		}
		if d, err := time.Parse(time.DateOnly, requested); err == nil && now.Sub(d) > settledAfter {
			return true
		}
	}
	return now.Sub(at) < LatestTTL
}

func (c *Client) fetch(ctx context.Context, from, to, requested string) (Quote, error) {
	base := strings.TrimRight(c.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 5 * time.Second}
	}
	u := base + "/" + requested + "?" + url.Values{"from": {from}, "to": {to}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Quote{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return Quote{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return Quote{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusUnprocessableEntity:
		return Quote{}, ErrUnsupported
	case resp.StatusCode >= 500:
		return Quote{}, fmt.Errorf("%w: HTTP %d", ErrUnavailable, resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return Quote{}, fmt.Errorf("%w: HTTP %d", ErrBadUpstream, resp.StatusCode)
	}
	var out struct {
		Base  string                 `json:"base"`
		Date  string                 `json:"date"`
		Rates map[string]json.Number `json:"rates"`
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		return Quote{}, fmt.Errorf("%w: %v", ErrBadUpstream, err)
	}
	r, ok := out.Rates[to]
	if !ok {
		return Quote{}, ErrUnsupported
	}
	rat, ok := new(big.Rat).SetString(r.String())
	if !ok || rat.Sign() <= 0 {
		return Quote{}, fmt.Errorf("%w: rate %q", ErrBadUpstream, r)
	}
	if _, err := time.Parse(time.DateOnly, out.Date); err != nil {
		return Quote{}, fmt.Errorf("%w: date %q", ErrBadUpstream, out.Date)
	}
	return Quote{From: from, To: to, Rate: FormatRate(rat), Date: out.Date, Source: Source}, nil
}

// RateSigDigits is the precision exchange_rate is stored with.
const RateSigDigits = 10

// FormatRate formats r as a plain decimal with up to RateSigDigits
// significant digits (half-up), trailing zeros trimmed.
func FormatRate(r *big.Rat) string {
	if r.Sign() <= 0 {
		return "0"
	}
	// Find the decimal exponent so that we keep RateSigDigits digits.
	intDigits := len(new(big.Int).Quo(r.Num(), r.Denom()).String())
	var decimals int
	if new(big.Int).Quo(r.Num(), r.Denom()).Sign() == 0 {
		// r < 1: count leading zeros after the point.
		x := new(big.Rat).Set(r)
		ten := big.NewRat(10, 1)
		lead := 0
		for x.Cmp(big.NewRat(1, 10)) < 0 {
			x.Mul(x, ten)
			lead++
		}
		decimals = lead + RateSigDigits
	} else {
		decimals = max(RateSigDigits-intDigits, 0)
	}
	s := roundHalfUp(r, decimals)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}

// roundHalfUp formats a non-negative r with exactly decimals digits after the point.
func roundHalfUp(r *big.Rat, decimals int) string {
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	x := new(big.Rat).Mul(r, new(big.Rat).SetInt(scale))
	x.Add(x, big.NewRat(1, 2))
	n := new(big.Int).Quo(x.Num(), x.Denom()) // floor for x >= 0
	s := n.String()
	if decimals == 0 {
		return s
	}
	if len(s) <= decimals {
		s = strings.Repeat("0", decimals-len(s)+1) + s
	}
	return s[:len(s)-decimals] + "." + s[len(s)-decimals:]
}

// Convert returns amountMinor (at scale fromScale) times rate, rounded half
// up to toScale, in minor units. Errors if the result is zero or out of range.
func Convert(amountMinor int64, fromScale int, rate string, toScale int) (int64, error) {
	r, ok := new(big.Rat).SetString(rate)
	if !ok || r.Sign() <= 0 {
		return 0, fmt.Errorf("invalid rate %q", rate)
	}
	x := new(big.Rat).SetFrac(big.NewInt(amountMinor), new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(fromScale)), nil))
	x.Mul(x, r)
	minor, err := money.ParseAmount(roundHalfUp(x, toScale), toScale)
	if err != nil {
		return 0, fmt.Errorf("converted amount %s", err)
	}
	return minor, nil
}

// ManualRate is toMinor/amountMinor as a rate (destination per 1 source unit).
func ManualRate(amountMinor int64, fromScale int, toMinor int64, toScale int) string {
	r := new(big.Rat).SetFrac(
		new(big.Int).Mul(big.NewInt(toMinor), new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(fromScale)), nil)),
		new(big.Int).Mul(big.NewInt(amountMinor), new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(toScale)), nil)))
	return FormatRate(r)
}
