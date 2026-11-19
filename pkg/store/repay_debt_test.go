package store

import (
	"context"
	"testing"

	"github.com/nutmos/expensetrackr/pkg/transaction"
)

func TestRepayDebtEffects(t *testing.T) {
	ctx := context.Background()
	t.Run("pay a credit card", func(t *testing.T) {
		f := newFixture(t)
		e := f.create(f.tx("repay_debt", "60", "THB", f.pay, &f.card))
		f.want(f.pay, "940.00", 2)
		f.want(f.card, "40.00", 2)
		if err := f.st.DeleteWith(ctx, f.userUID, e.ID, WriteOptions{}); err != nil {
			t.Fatal(err)
		}
		f.want(f.pay, "1000.00", 3)
		f.want(f.card, "100.00", 3)
	})
	t.Run("repay a loan, overpaying a card leaves it in credit", func(t *testing.T) {
		f := newFixture(t)
		f.create(f.tx("repay_debt", "500", "THB", f.pay, &f.loan))
		f.want(f.pay, "500.00", 2)
		f.want(f.loan, "4500.00", 2)
		f.create(f.tx("repay_debt", "150", "THB", f.pay, &f.card))
		f.want(f.card, "-50.00", 2)
	})
	t.Run("edit to a transfer and back", func(t *testing.T) {
		f := newFixture(t)
		e := f.create(f.tx("repay_debt", "100", "THB", f.pay, &f.loan))
		update := func(next transaction.Transaction) {
			t.Helper()
			if _, err := f.st.UpdateWith(ctx, f.userUID, e.ID, WriteOptions{}, func(transaction.Transaction) (transaction.Transaction, error) { return next, nil }); err != nil {
				t.Fatalf("update: %v", err)
			}
		}
		update(f.tx("transfer", "100", "THB", f.pay, &f.loan))
		f.want(f.pay, "900.00", 2) // same effect: not written
		f.want(f.loan, "4900.00", 2)
		update(f.tx("repay_debt", "40", "THB", f.pay, &f.card))
		f.want(f.pay, "960.00", 3)
		f.want(f.loan, "5000.00", 3)
		f.want(f.card, "60.00", 2)
		got, err := f.st.GetByUID(ctx, f.userUID, e.UID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Type != transaction.RepayDebt || got.CategoryUID != nil || got.ToAccount == nil || *got.ToAccount != "card" {
			t.Errorf("stored = %+v", got)
		}
	})
}

// TestMigrateV2ToV3: a v2 database (whose type check has no repay_debt)
// accepts repay_debt rows after the upgrade and keeps its existing rows.
func TestMigrateV2ToV3(t *testing.T) {
	url := testURL(t)
	buildV1(t, url)
	db := rawDB(t, url)
	if _, err := db.Exec(`DROP INDEX balances_user_name_uq; INSERT INTO schema_migrations (version) VALUES (2)`); err != nil {
		t.Fatal(err)
	}
	const owner = "00000000-0000-4000-8000-000000000001"
	if _, err := db.Exec(`INSERT INTO users (uid, display_name, created_at) VALUES ($1, 'Owner', now())`, owner); err != nil {
		t.Fatal(err)
	}
	insert := func(typ string) error {
		_, err := db.Exec(`INSERT INTO transactions (uid, user_uid, type, amount_minor, amount_scale, currency, balance_uid, account, spent_at, spent_at_utc, created_at)
			VALUES (gen_random_uuid(), $1, $2, 100, 2, 'THB', '00000000-0000-4000-8000-0000000000b1', 'Cash', '2026-10-08T12:00:00+08:00', now(), now())`, owner, typ)
		return err
	}
	if err := insert("repay_debt"); err == nil {
		t.Fatal("v2 accepted repay_debt; the test setup is wrong")
	}
	if err := insert("expense"); err != nil {
		t.Fatal(err)
	}

	st, err := Open(url)
	if err != nil {
		t.Fatalf("open v2: %v", err)
	}
	defer st.Close()
	if v := schemaVersion(t, url); v != SchemaVersion {
		t.Errorf("schema version after upgrade = %d, want %d", v, SchemaVersion)
	}
	if err := insert("repay_debt"); err != nil {
		t.Errorf("repay_debt after upgrade: %v", err)
	}
	if err := insert("bogus"); err == nil {
		t.Error("unknown type accepted after upgrade")
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM transactions`).Scan(&n); err != nil || n != 2 {
		t.Errorf("rows = %d (%v), want 2", n, err)
	}

	fresh := testURL(t)
	fst, err := Open(fresh)
	if err != nil {
		t.Fatal(err)
	}
	fst.Close()
	if got, want := portableSchema(t, url), portableSchema(t, fresh); got != want {
		t.Errorf("upgraded schema differs from a fresh one:\n got %v\nwant %v", got, want)
	}
}
