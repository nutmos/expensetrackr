package transaction

import (
	"errors"
	"testing"
	"time"

	"github.com/nutmos/expensetrackr/pkg/balance"
	"github.com/nutmos/expensetrackr/pkg/fx"
)

var (
	usdBal = balance.Balance{UID: "11111111-1111-4111-8111-111111111111", Name: "Dollars", Type: balance.TypePaymentAccount, Currency: "USD"}
	thbBal = balance.Balance{UID: "22222222-2222-4222-8222-222222222222", Name: "Baht", Type: balance.TypePaymentAccount, Currency: "THB"}
	usd2   = balance.Balance{UID: "33333333-3333-4333-8333-333333333333", Name: "Dollars 2", Type: balance.TypePaymentAccount, Currency: "USD"}
)

// usdTransfer is a validated 100.00 USD transfer from usdBal to dst.
func usdTransfer(t *testing.T, dst balance.Balance, spentAt string) Transaction {
	t.Helper()
	e, err := CreateInput{Type: "transfer", Amount: "100", Currency: "USD", BalanceUID: string(usdBal.UID),
		ToBalanceUID: string(dst.UID), SpentAt: spentAt}.Validate()
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// stubRate answers USD->THB 33.615 dated rateDate and counts calls.
type stubRate struct {
	calls    int
	gotDate  string
	gotLoc   *time.Location
	rateDate string
	err      error
}

func (s *stubRate) rate(from, to, date string, loc *time.Location) (fx.Quote, error) {
	s.calls++
	s.gotDate, s.gotLoc = date, loc
	if s.err != nil {
		return fx.Quote{}, s.err
	}
	return fx.Quote{From: from, To: to, Rate: "33.615", Date: s.rateDate, Source: fx.Source}, nil
}

func fieldOf(t *testing.T, err error) string {
	t.Helper()
	var ve *ValidationError
	if !errors.As(err, &ve) || len(ve.Fields) != 1 {
		t.Fatalf("want one-field ValidationError, got %v", err)
	}
	for k := range ve.Fields {
		return k
	}
	return ""
}

func TestConvertTransferFetchesRate(t *testing.T) {
	e := usdTransfer(t, thbBal, "2026-10-10T09:00:00+07:00")
	r := &stubRate{rateDate: "2026-10-09"}
	if err := e.ConvertTransfer(CreateInput{}, usdBal, thbBal, nil, r.rate); err != nil {
		t.Fatal(err)
	}
	if r.calls != 1 || r.gotDate != "2026-10-10" {
		t.Errorf("rate calls %d, date %q (want the spent_at date as entered)", r.calls, r.gotDate)
	}
	if _, off := time.Unix(0, 0).In(r.gotLoc).Zone(); off != 7*3600 {
		t.Errorf("rate loc offset %d, want +07:00", off)
	}
	if *e.ToCurrency != "THB" || *e.ToAmount != "3361.50" || *e.ToAmountMinor != 336150 ||
		*e.ExchangeRate != "33.615" || *e.RateSource != fx.Source || *e.RateDate != "2026-10-09" {
		t.Errorf("converted: %s %s %d %s %s %s", *e.ToCurrency, *e.ToAmount, *e.ToAmountMinor, *e.ExchangeRate, *e.RateSource, *e.RateDate)
	}
}

func TestConvertTransferManualAmount(t *testing.T) {
	e := usdTransfer(t, thbBal, "2026-10-10T09:00:00Z")
	r := &stubRate{}
	if err := e.ConvertTransfer(CreateInput{ToAmount: "3400", ToCurrency: "thb"}, usdBal, thbBal, nil, r.rate); err != nil {
		t.Fatal(err)
	}
	if r.calls != 0 || *e.ToAmount != "3400.00" || *e.ExchangeRate != "34" || *e.RateSource != "manual" || e.RateDate != nil {
		t.Errorf("manual: calls %d, %s %s %s %v", r.calls, *e.ToAmount, *e.ExchangeRate, *e.RateSource, e.RateDate)
	}
	if err := e.ConvertTransfer(CreateInput{ToAmount: "-1"}, usdBal, thbBal, nil, r.rate); fieldOf(t, err) != "to_amount" {
		t.Errorf("bad to_amount: %v", err)
	}
}

func TestConvertTransferKeepsUnchangedConversion(t *testing.T) {
	prev := usdTransfer(t, thbBal, "2026-10-10T09:00:00Z")
	if err := prev.ConvertTransfer(CreateInput{}, usdBal, thbBal, nil, (&stubRate{rateDate: "2026-10-09"}).rate); err != nil {
		t.Fatal(err)
	}
	// Same date (later time) and amounts: the stored conversion is kept.
	next := usdTransfer(t, thbBal, "2026-10-10T20:00:00Z")
	r := &stubRate{err: fx.ErrUnavailable}
	if err := next.ConvertTransfer(CreateInput{}, usdBal, thbBal, &prev, r.rate); err != nil || r.calls != 0 {
		t.Fatalf("unchanged: %v, calls %d", err, r.calls)
	}
	if *next.ToAmountMinor != *prev.ToAmountMinor || *next.RateDate != "2026-10-09" {
		t.Errorf("kept conversion: %d %s", *next.ToAmountMinor, *next.RateDate)
	}
	// Another date: a new rate is needed, and its error is returned as is.
	moved := usdTransfer(t, thbBal, "2026-10-11T09:00:00Z")
	if err := moved.ConvertTransfer(CreateInput{}, usdBal, thbBal, &prev, r.rate); !errors.Is(err, fx.ErrUnavailable) || r.calls != 1 {
		t.Errorf("changed date: %v, calls %d", err, r.calls)
	}
}

func TestConvertTransferRules(t *testing.T) {
	r := &stubRate{}
	// Same currency: nothing stored; to_amount must equal amount.
	e := usdTransfer(t, usd2, "2026-10-10T09:00:00Z")
	if err := e.ConvertTransfer(CreateInput{ToAmount: "100.00"}, usdBal, usd2, nil, r.rate); err != nil || e.ToCurrency != nil {
		t.Errorf("same currency: %v %v", err, e.ToCurrency)
	}
	if err := e.ConvertTransfer(CreateInput{ToAmount: "99"}, usdBal, usd2, nil, r.rate); fieldOf(t, err) != "to_amount" {
		t.Errorf("same currency, other amount: %v", err)
	}
	// to_currency must be the destination's; the source must match the
	// transaction currency. Neither needs a rate.
	e = usdTransfer(t, thbBal, "2026-10-10T09:00:00Z")
	if err := e.ConvertTransfer(CreateInput{ToCurrency: "EUR"}, usdBal, thbBal, nil, r.rate); fieldOf(t, err) != "to_currency" {
		t.Errorf("to_currency: %v", err)
	}
	if err := e.ConvertTransfer(CreateInput{}, thbBal, thbBal, nil, r.rate); fieldOf(t, err) != "balance_uid" {
		t.Errorf("source currency: %v", err)
	}
	if r.calls != 0 {
		t.Errorf("rate called %d times for rejected input", r.calls)
	}
	// Not a transfer: stale cross-currency fields are cleared.
	ap := usdTransfer(t, thbBal, "2026-10-10T09:00:00Z")
	if err := ap.ConvertTransfer(CreateInput{}, usdBal, thbBal, nil, (&stubRate{}).rate); err != nil {
		t.Fatal(err)
	}
	ap.Type = AssetPurchase
	if err := ap.ConvertTransfer(CreateInput{}, usdBal, thbBal, nil, r.rate); err != nil || ap.ToCurrency != nil || ap.ToAmountMinor != nil || ap.ExchangeRate != nil {
		t.Errorf("non-transfer keeps fx fields: %v %+v", err, ap)
	}
}

func TestLocalDate(t *testing.T) {
	for in, want := range map[string]string{"2026-10-10T23:30:00-05:00": "2026-10-10", "2026-10-10": "2026-10-10", "short": "short", "": ""} {
		if got := LocalDate(in); got != want {
			t.Errorf("LocalDate(%q) = %q, want %q", in, got, want)
		}
	}
}
