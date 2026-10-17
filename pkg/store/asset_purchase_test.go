package store

import (
	"context"
	"errors"
	"testing"

	"github.com/nutmos/expensetrackr/pkg/transaction"
	"github.com/nutmos/expensetrackr/pkg/validate"
)

func TestAssetPurchaseEffects(t *testing.T) {
	ctx := context.Background()
	t.Run("from payment account", func(t *testing.T) {
		f := newFixture(t)
		e := f.create(f.tx("asset_purchase", "200", "THB", f.pay, &f.gold))
		f.want(f.pay, "800.00", 2)
		f.want(f.gold, "700.00", 2)
		if err := f.st.Delete(ctx, e.ID); err != nil {
			t.Fatal(err)
		}
		f.want(f.pay, "1000.00", 3)
		f.want(f.gold, "500.00", 3)
	})
	t.Run("from credit card: debt up", func(t *testing.T) {
		f := newFixture(t)
		e := f.create(f.tx("asset_purchase", "300", "THB", f.card, &f.gold))
		f.want(f.card, "400.00", 2)
		f.want(f.gold, "800.00", 2)
		if err := f.st.Delete(ctx, e.ID); err != nil {
			t.Fatal(err)
		}
		f.want(f.card, "100.00", 3)
		f.want(f.gold, "500.00", 3)
		f.want(f.pay, "1000.00", 1)
	})
	t.Run("edit amount and type changes", func(t *testing.T) {
		f := newFixture(t)
		e := f.create(f.tx("asset_purchase", "200", "THB", f.pay, &f.gold))
		update := func(next transaction.Transaction) {
			t.Helper()
			if _, err := f.st.Update(ctx, e.ID, func(transaction.Transaction) (transaction.Transaction, error) { return next, nil }); err != nil {
				t.Fatalf("update: %v", err)
			}
		}
		update(f.tx("asset_purchase", "50", "THB", f.pay, &f.gold))
		f.want(f.pay, "950.00", 3)
		f.want(f.gold, "550.00", 3)
		// asset_purchase -> expense: the asset goes back, the account pays 50.
		update(f.tx("expense", "50", "THB", f.pay, nil))
		f.want(f.pay, "950.00", 3) // net zero: not written
		f.want(f.gold, "500.00", 4)
		// expense -> asset_purchase on the card.
		update(f.tx("asset_purchase", "70", "THB", f.card, &f.gold))
		f.want(f.pay, "1000.00", 4)
		f.want(f.card, "170.00", 2)
		f.want(f.gold, "570.00", 5)
		// asset_purchase -> transfer card -> loan.
		update(f.tx("transfer", "70", "THB", f.card, &f.loan))
		f.want(f.card, "170.00", 2)
		f.want(f.gold, "500.00", 6)
		f.want(f.loan, "4930.00", 2)
	})
	t.Run("currency mismatch", func(t *testing.T) {
		f := newFixture(t)
		e := f.tx("asset_purchase", "10", "USD", f.pay, &f.gold)
		var ve *validate.ValidationError
		if err := f.st.Create(ctx, &e); !errors.As(err, &ve) {
			t.Fatalf("want validation error, got %v", err)
		}
		f.want(f.pay, "1000.00", 1)
		f.want(f.gold, "500.00", 1)
	})
}
