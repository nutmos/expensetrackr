package store

import (
	"context"
	"testing"

	"github.com/nutmos/expensetrackr/pkg/transaction"
)

// TestForeignAmountStored: the foreign amount round-trips through create,
// get, list and update, and never moves a balance.
func TestForeignAmountStored(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	withForeign := func(e transaction.Transaction, amt string, minor int64, cur string) transaction.Transaction {
		e.ForeignAmount, e.ForeignAmountMinor, e.ForeignCurrency = &amt, &minor, &cur
		return e
	}
	e := f.create(withForeign(f.tx("expense", "850", "THB", f.pay, nil), "25.00", 2500, "USD"))
	f.want(f.pay, "150.00", 2) // only amount (THB) moves the balance

	got, err := f.st.GetByUID(ctx, f.userUID, e.UID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ForeignAmount == nil || *got.ForeignAmount != "25.00" || *got.ForeignAmountMinor != 2500 || *got.ForeignCurrency != "USD" {
		t.Fatalf("stored foreign = %v %v %v", got.ForeignAmount, got.ForeignAmountMinor, got.ForeignCurrency)
	}

	// Change only the foreign amount: the balance is not touched.
	upd, err := f.st.UpdateWith(ctx, f.userUID, e.ID, WriteOptions{}, func(transaction.Transaction) (transaction.Transaction, error) {
		return withForeign(f.tx("expense", "850", "THB", f.pay, nil), "1500", 1500, "JPY"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if *upd.ForeignAmount != "1500" || *upd.ForeignCurrency != "JPY" {
		t.Errorf("updated foreign = %v %v", *upd.ForeignAmount, *upd.ForeignCurrency)
	}
	f.want(f.pay, "150.00", 2)

	// Clear it.
	if _, err := f.st.UpdateWith(ctx, f.userUID, e.ID, WriteOptions{}, func(transaction.Transaction) (transaction.Transaction, error) {
		return f.tx("expense", "850", "THB", f.pay, nil), nil
	}); err != nil {
		t.Fatal(err)
	}
	list, err := withoutBeginning(f.st.List(ctx, f.userUID, ListFilter{}))
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v %d", err, len(list))
	}
	if list[0].ForeignAmount != nil || list[0].ForeignAmountMinor != nil || list[0].ForeignCurrency != nil {
		t.Errorf("after clear: %v %v %v", list[0].ForeignAmount, list[0].ForeignAmountMinor, list[0].ForeignCurrency)
	}
}

// TestForeignAmountInvariants: the store refuses inconsistent foreign fields
// even when validation was bypassed.
func TestForeignAmountInvariants(t *testing.T) {
	f := newFixture(t)
	amt, minor := "10.00", int64(1000)
	cases := map[string]func() transaction.Transaction{
		"on a transfer": func() transaction.Transaction {
			e := f.tx("transfer", "10", "THB", f.pay, &f.gold)
			cur := "USD"
			e.ForeignAmount, e.ForeignAmountMinor, e.ForeignCurrency = &amt, &minor, &cur
			return e
		},
		"same currency": func() transaction.Transaction {
			e := f.tx("expense", "10", "THB", f.pay, nil)
			cur := "THB"
			e.ForeignAmount, e.ForeignAmountMinor, e.ForeignCurrency = &amt, &minor, &cur
			return e
		},
		"currency missing": func() transaction.Transaction {
			e := f.tx("expense", "10", "THB", f.pay, nil)
			e.ForeignAmount, e.ForeignAmountMinor = &amt, &minor
			return e
		},
	}
	for name, mk := range cases {
		e := mk()
		if err := f.st.Create(context.Background(), f.userUID, &e); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	f.want(f.pay, "1000.00", 1)
}

// TestMigrateV3ToV4: a v3 database gains the foreign amount columns, keeps
// its rows, and ends up with the same schema as a fresh one.
func TestMigrateV3ToV4(t *testing.T) {
	url := testURL(t)
	buildV1(t, url)
	db := rawDB(t, url)
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations[1:3] {
		if err := m(ctx, tx); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO schema_migrations (version) VALUES (2), (3)`); err != nil {
		t.Fatal(err)
	}
	const owner = "00000000-0000-4000-8000-000000000001"
	if _, err := db.Exec(`INSERT INTO users (uid, display_name, created_at) VALUES ($1, 'Owner', now())`, owner); err != nil {
		t.Fatal(err)
	}
	insert := func(typ, foreign string) error {
		cols, vals := "", ""
		if foreign != "" {
			cols, vals = ", foreign_amount_minor, foreign_amount_scale, foreign_currency", ", 2500, 2, '"+foreign+"'"
		}
		_, err := db.Exec(`INSERT INTO transactions (uid, user_uid, type, amount_minor, amount_scale, currency, balance_uid, account, spent_at, spent_at_utc, created_at`+cols+`)
			VALUES (gen_random_uuid(), $1, $2, 100, 2, 'THB', '00000000-0000-4000-8000-0000000000b1', 'Cash', '2026-10-08T12:00:00+08:00', now(), now()`+vals+`)`, owner, typ)
		return err
	}
	if err := insert("expense", ""); err != nil {
		t.Fatal(err)
	}
	if err := insert("expense", "USD"); err == nil {
		t.Fatal("v3 accepted foreign columns; the test setup is wrong")
	}

	st, err := Open(url)
	if err != nil {
		t.Fatalf("open v3: %v", err)
	}
	defer st.Close()
	if v := schemaVersion(t, url); v != SchemaVersion {
		t.Errorf("schema version after upgrade = %d, want %d", v, SchemaVersion)
	}
	if err := insert("income", "USD"); err != nil {
		t.Errorf("foreign amount after upgrade: %v", err)
	}
	if err := insert("expense", "THB"); err == nil {
		t.Error("foreign_currency equal to currency accepted")
	}
	if err := insert("transfer", "USD"); err == nil {
		t.Error("foreign amount on a transfer accepted")
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
