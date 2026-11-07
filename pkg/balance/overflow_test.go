package balance

import (
	"errors"
	"math"
	"testing"
)

func i64(v int64) *int64 { return &v }

func asset(cur string, scale int, bal int64) Balance {
	b := Balance{Type: TypePaymentAccount, Currency: cur}
	b.SetAmounts(scale, i64(bal), nil, nil)
	return b
}

func card(cur string, scale int, debt, limit int64) Balance {
	b := Balance{Type: TypeCreditCard, Currency: cur}
	b.SetAmounts(scale, nil, i64(debt), i64(limit))
	return b
}

// TestComputeTotalsDoesNotOverflow: totals past the int64 range are still
// exact, not wrapped around to a negative number.
func TestComputeTotalsDoesNotOverflow(t *testing.T) {
	items := []Balance{asset("THB", 2, math.MaxInt64), asset("THB", 2, math.MaxInt64), card("THB", 2, math.MinInt64+1, math.MaxInt64)}
	got := ComputeTotals(items)
	if len(got) != 1 {
		t.Fatalf("totals: %+v", got)
	}
	tt := got[0]
	// 2 × 9223372036854775807 = 18446744073709551614 minor units.
	if tt.Assets != "184467440737095516.14" {
		t.Errorf("assets %s", tt.Assets)
	}
	if tt.Liabilities != "-92233720368547758.07" {
		t.Errorf("liabilities %s", tt.Liabilities)
	}
	// net = assets - liabilities; available = limit - liabilities.
	if tt.Net != "276701161105643274.21" || tt.CreditLimit != "92233720368547758.07" || tt.AvailableCredit != "184467440737095516.14" {
		t.Errorf("net %s, limit %s, available %s", tt.Net, tt.CreditLimit, tt.AvailableCredit)
	}
}

// TestComputeTotalsSmall keeps ordinary totals exact (and sorted).
func TestComputeTotalsSmall(t *testing.T) {
	got := ComputeTotals([]Balance{asset("THB", 2, 150), card("THB", 2, 50, 1000), asset("JPY", 0, 7), asset("THB", 2, -25)})
	if len(got) != 2 || got[0].Currency != "JPY" || got[0].Assets != "7" || got[0].Net != "7" {
		t.Fatalf("JPY: %+v", got)
	}
	thb := got[1]
	if thb.Assets != "1.25" || thb.Liabilities != "0.50" || thb.Net != "0.75" || thb.CreditLimit != "10.00" || thb.AvailableCredit != "9.50" {
		t.Errorf("THB: %+v", thb)
	}
}

func TestApplyValueDelta(t *testing.T) {
	a := asset("THB", 2, 1000)
	if err := a.ApplyValueDelta(-1500); err != nil || *a.BalanceMinor != -500 || *a.Balance != "-5.00" {
		t.Errorf("asset: %v %v", err, *a.BalanceMinor)
	}
	c := card("THB", 2, 200, 1000)
	// A positive delta (payment to the card) lowers the debt.
	if err := c.ApplyValueDelta(300); err != nil || *c.DebtMinor != -100 || *c.AvailableMinor != 1100 || *c.OverLimit {
		t.Errorf("card payment: %v debt %v avail %v", err, *c.DebtMinor, *c.AvailableMinor)
	}
	if err := c.ApplyValueDelta(-1200); err != nil || *c.DebtMinor != 1100 || *c.AvailableMinor != -100 || !*c.OverLimit {
		t.Errorf("card spend: %v debt %v avail %v over %v", err, *c.DebtMinor, *c.AvailableMinor, *c.OverLimit)
	}

	// Results outside int64 are refused and leave the balance unchanged.
	big := asset("THB", 2, math.MaxInt64-1)
	if err := big.ApplyValueDelta(2); !errors.Is(err, ErrOverflow) || *big.BalanceMinor != math.MaxInt64-1 {
		t.Errorf("asset overflow: %v %v", err, *big.BalanceMinor)
	}
	zero := card("THB", 2, 0, 0)
	if err := zero.ApplyValueDelta(math.MinInt64); !errors.Is(err, ErrOverflow) {
		t.Errorf("MinInt64 delta: %v", err)
	}
	// available = limit - debt must stay representable too: overpaying a
	// card so far that it would wrap is refused.
	payer := card("THB", 2, -10, math.MaxInt64)
	if err := payer.ApplyValueDelta(1); !errors.Is(err, ErrOverflow) || *payer.DebtMinor != -10 {
		t.Errorf("available overflow: %v debt %v", err, *payer.DebtMinor)
	}

	// A balance without an amount for its type is an error, not a panic.
	if err := (&Balance{Type: TypeCreditCard}).ApplyValueDelta(1); err == nil || errors.Is(err, ErrOverflow) {
		t.Errorf("no amount: %v", err)
	}
}

func TestValidateUpdate(t *testing.T) {
	// Same type (any case/spacing): an ordinary validation.
	b, err := Input{Name: "Visa", Type: " Credit_Card ", Currency: "THB", Debt: dec("10"), Limit: dec("100")}.ValidateUpdate(TypeCreditCard)
	if err != nil || b.Type != TypeCreditCard || *b.DebtMinor != 1000 {
		t.Fatalf("same type: %v %+v", err, b)
	}
	// Another type: "type" is refused, and the other fields are still
	// checked against the stored type so every problem is reported at once.
	_, err = Input{Name: "", Type: "payment_account", Currency: "THB", Balance: dec("5")}.ValidateUpdate(TypeCreditCard)
	f := fieldErrs(t, err)
	if f["type"] != ErrTypeChanged || f["name"] == "" || f["debt"] == "" || f["limit"] == "" || f["balance"] == "" {
		t.Errorf("type change: %v", f)
	}
	// No type at all is "required", not a change.
	if f := fieldErrs(t, func() error { _, err := (Input{Name: "x", Currency: "THB"}).ValidateUpdate(TypeOtherAsset); return err }()); f["type"] == "" || f["type"] == ErrTypeChanged {
		t.Errorf("missing type: %v", f)
	}
}

// TestInputRoundTrip: a stored balance turned back into an Input validates
// to the same values (used to build PUT bodies).
func TestInputRoundTrip(t *testing.T) {
	for _, in := range []Input{
		{Name: "Wallet", Type: "payment_account", Currency: "THB", Description: "cash", Balance: dec("-12.50")},
		{Name: "Visa", Type: "credit_card", Currency: "JPY", Debt: dec("1500"), Limit: dec("300000")},
	} {
		b, err := in.Validate()
		if err != nil {
			t.Fatal(err)
		}
		again, err := b.Input().Validate()
		if err != nil {
			t.Fatalf("%s: %v", in.Name, err)
		}
		if again.Name != b.Name || again.Type != b.Type || again.Currency != b.Currency || again.Description != b.Description ||
			fmtPtr(again.Balance) != fmtPtr(b.Balance) || fmtPtr(again.Debt) != fmtPtr(b.Debt) || fmtPtr(again.Limit) != fmtPtr(b.Limit) {
			t.Errorf("%s: %+v != %+v", in.Name, again, b)
		}
	}
}

func fmtPtr(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}
