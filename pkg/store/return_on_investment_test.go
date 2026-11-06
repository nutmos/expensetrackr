package store

import (
	"context"
	"errors"
	"testing"

	"github.com/nutmos/expensetrackr/pkg/balance"
	"github.com/nutmos/expensetrackr/pkg/transaction"
	"github.com/nutmos/expensetrackr/pkg/validate"
)

const roiType = string(transaction.ReturnOnInvestment)

// TestReturnOnInvestmentEffects: a return on investment moves only the
// receiving account or card, like income; the linked other_asset never moves.
// Fixture amounts: pay 1000.00, card debt 100.00, gold 500.00.
func TestReturnOnInvestmentEffects(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name       string
		amount     string
		into       func(*fixture) balance.Balance
		wantAfter  string // receiving balance (or card debt) after create
		wantBefore string // after delete
	}{
		{"into payment_account: balance up", "25", func(f *fixture) balance.Balance { return f.pay }, "1025.00", "1000.00"},
		{"into credit_card: debt down", "40", func(f *fixture) balance.Balance { return f.card }, "60.00", "100.00"},
		{"over card debt: card in credit", "150", func(f *fixture) balance.Balance { return f.card }, "-50.00", "100.00"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			into := tc.into(f)
			e := f.create(f.tx(roiType, tc.amount, "THB", into, &f.gold))
			if !e.AdjustsBalances {
				t.Error("adjusts_balances = false, want true")
			}
			f.want(into, tc.wantAfter, 2)
			f.want(f.gold, "500.00", 1)
			if err := f.st.DeleteWith(ctx, f.userUID, e.ID, WriteOptions{}); err != nil {
				t.Fatal(err)
			}
			f.want(into, tc.wantBefore, 3)
			f.want(f.gold, "500.00", 1)
		})
	}

	t.Run("edit and type changes", func(t *testing.T) {
		f := newFixture(t)
		e := f.create(f.tx(roiType, "20", "THB", f.pay, &f.gold))
		update := func(next transaction.Transaction) {
			t.Helper()
			if _, err := f.st.UpdateWith(ctx, f.userUID, e.ID, WriteOptions{}, func(transaction.Transaction) (transaction.Transaction, error) { return next, nil }); err != nil {
				t.Fatalf("update: %v", err)
			}
		}
		update(f.tx(roiType, "50", "THB", f.pay, &f.gold))
		f.want(f.pay, "1050.00", 3)
		// Move the return to the card: pay back to 1000, card debt down 50.
		update(f.tx(roiType, "50", "THB", f.card, &f.gold))
		f.want(f.pay, "1000.00", 4)
		f.want(f.card, "50.00", 2)
		// return_on_investment -> income: same effect, nothing written.
		update(f.tx("income", "50", "THB", f.card, nil))
		f.want(f.card, "50.00", 2)
		// income -> return_on_investment -> asset_purchase: now the asset moves.
		update(f.tx(roiType, "50", "THB", f.card, &f.gold))
		update(f.tx("asset_purchase", "50", "THB", f.card, &f.gold))
		f.want(f.card, "150.00", 3)
		f.want(f.gold, "550.00", 2)
		update(f.tx(roiType, "50", "THB", f.card, &f.gold))
		f.want(f.card, "50.00", 4)
		f.want(f.gold, "500.00", 3)
	})

	t.Run("currency must match the receiving account", func(t *testing.T) {
		f := newFixture(t)
		e := f.tx(roiType, "10", "USD", f.pay, &f.gold)
		var ve *validate.ValidationError
		if err := f.st.Create(ctx, f.userUID, &e); !errors.As(err, &ve) || ve.Fields["balance_uid"] == "" {
			t.Fatalf("want balance_uid validation error, got %v", err)
		}
		f.want(f.pay, "1000.00", 1)
	})

	t.Run("linked asset in another currency is allowed", func(t *testing.T) {
		f := newFixture(t)
		fund := mustBalance(t, balance.Input{Name: "Fund", Type: "other_asset", Currency: "USD", Balance: dec("300")})
		if err := f.st.CreateBalance(ctx, f.userUID, &fund); err != nil {
			t.Fatal(err)
		}
		f.create(f.tx(roiType, "10", "THB", f.pay, &fund))
		f.want(f.pay, "1010.00", 2)
		f.want(fund, "300.00", 1)
	})

	t.Run("filter by type", func(t *testing.T) {
		f := newFixture(t)
		f.create(f.tx(roiType, "10", "THB", f.pay, &f.gold))
		f.create(f.tx("income", "10", "THB", f.pay, nil))
		got, err := f.st.List(ctx, f.userUID, ListFilter{Type: transaction.ReturnOnInvestment})
		if err != nil || len(got) != 1 || got[0].Type != transaction.ReturnOnInvestment {
			t.Fatalf("list: %+v %v", got, err)
		}
	})
}
