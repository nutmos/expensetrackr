package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
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

// TestMigrationV15ToV16 rebuilds a v15 transactions table (type CHECK
// without 'asset_purchase') and checks every row, the id sequence and the
// schema survive the upgrade, and that it is idempotent.
func TestMigrationV15ToV16(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v15.db")
	f := newFixture(t)
	f.create(f.tx("expense", "10", "THB", f.pay, nil))
	f.create(f.tx("income", "20", "THB", f.gold, nil))
	del := f.create(f.tx("transfer", "30", "THB", f.pay, &f.card))
	f.create(f.tx("expense", "40", "THB", f.card, nil))
	if err := f.st.Delete(ctx, del.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.db.Exec(`VACUUM INTO ?`, path); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	old := strings.Replace(transactionsTable("transactions_old"), "'asset_purchase', ", "", 1)
	for _, q := range []string{
		old,
		`INSERT INTO transactions_old SELECT * FROM transactions`,
		`DROP TABLE transactions`,
		`ALTER TABLE transactions_old RENAME TO transactions`,
		transactionsIndexes,
		`UPDATE sqlite_sequence SET seq = 4 WHERE name = 'transactions'`,
		`DELETE FROM sqlite_sequence WHERE name = 'transactions_old'`,
		`PRAGMA user_version = 15`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if _, err := db.Exec(`UPDATE transactions SET type = 'asset_purchase' WHERE id = 1`); err == nil {
		t.Fatal("v15 fixture accepts asset_purchase")
	}
	db.Close()
	before := dumpTransactions(t, path)
	if len(before) != 3 {
		t.Fatalf("fixture rows %v", before)
	}

	st, err := Open(path)
	if err != nil {
		t.Fatalf("open v15: %v", err)
	}
	st.Close()
	if after := dumpTransactions(t, path); strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Errorf("rows changed:\n got %v\nwant %v", after, before)
	}
	if s := sequenceOf(t, path, "transactions"); s != 4 {
		t.Errorf("sequence %d, want 4", s)
	}
	assertCurrentSchema(t, path)

	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	pay, _ := st.GetBalanceByUID(ctx, f.pay.UID)
	gold, _ := st.GetBalanceByUID(ctx, f.gold.UID)
	g := &fixture{t: t, st: st, pay: pay, gold: gold}
	e := g.create(g.tx("asset_purchase", "5", "THB", pay, &gold))
	if e.ID != 5 {
		t.Errorf("new id %d, want 5", e.ID)
	}
	st.Close()

	// Idempotent re-run.
	before = dumpTransactions(t, path)
	db, err = sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := rebuildTransactionsV16(tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if after := dumpTransactions(t, path); strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Errorf("re-run changed rows")
	}
}
