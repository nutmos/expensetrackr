package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/nutmos/expensetrackr/pkg/balance"
	"github.com/nutmos/expensetrackr/pkg/transaction"
	"github.com/nutmos/expensetrackr/pkg/validate"
)

// fixture opens a fresh store with four THB balances:
//
//	pay   payment_account  balance 1000.00
//	card  credit_card      debt 100.00, limit 1000.00
//	gold  other_asset      balance 500.00
//	loan  other_liability  debt 5000.00, limit 10000.00
type fixture struct {
	t                     *testing.T
	st                    *Store
	pay, card, gold, loan balance.Balance
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "adjust.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := &fixture{t: t, st: st}
	mk := func(in balance.Input) balance.Balance {
		b := mustBalance(t, in)
		if err := st.CreateBalance(context.Background(), &b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	f.pay = mk(balance.Input{Name: "pay", Type: "payment_account", Currency: "THB", Balance: dec("1000")})
	f.card = mk(balance.Input{Name: "card", Type: "credit_card", Currency: "THB", Debt: dec("100"), Limit: dec("1000")})
	f.gold = mk(balance.Input{Name: "gold", Type: "other_asset", Currency: "THB", Balance: dec("500")})
	f.loan = mk(balance.Input{Name: "loan", Type: "other_liability", Currency: "THB", Debt: dec("5000"), Limit: dec("10000")})
	return f
}

// amount returns "balance" for assets and "debt" for liabilities, plus version.
func (f *fixture) amount(b balance.Balance) (string, int64) {
	f.t.Helper()
	got, err := f.st.GetBalanceByUID(context.Background(), b.UID)
	if err != nil {
		f.t.Fatal(err)
	}
	if got.Balance != nil {
		return *got.Balance, got.Version
	}
	return *got.Debt, got.Version
}

func (f *fixture) want(b balance.Balance, amount string, version int64) {
	f.t.Helper()
	if got, v := f.amount(b); got != amount || v != version {
		f.t.Errorf("%s = %s (v%d), want %s (v%d)", b.Name, got, v, amount, version)
	}
}

func (f *fixture) tx(typ, amount, currency string, from balance.Balance, to *balance.Balance) transaction.Transaction {
	f.t.Helper()
	in := transaction.CreateInput{Type: typ, Amount: transaction.DecimalInput(amount), Currency: currency, BalanceUID: from.UID, SpentAt: "2026-10-08T12:00:00+08:00"}
	if to != nil {
		in.ToBalanceUID = to.UID
	}
	e, err := in.Validate()
	if err != nil {
		f.t.Fatal(err)
	}
	e.Account = from.Name
	if to != nil {
		e.ToAccount = &to.Name
	}
	return e
}

func (f *fixture) create(e transaction.Transaction) transaction.Transaction {
	f.t.Helper()
	if err := f.st.Create(context.Background(), &e); err != nil {
		f.t.Fatalf("create: %v", err)
	}
	return e
}

func TestBalanceSignRules(t *testing.T) {
	type want struct {
		name    string // pay | card | gold | loan
		amount  string
		touched bool
	}
	cases := []struct {
		name     string
		typ, amt string
		from, to string
		want     []want
	}{
		{"expense from payment_account: balance down", "expense", "30", "pay", "", []want{{"pay", "970.00", true}}},
		{"expense on credit_card: debt up", "expense", "30", "card", "", []want{{"card", "130.00", true}}},
		{"income into payment_account: balance up", "income", "50", "pay", "", []want{{"pay", "1050.00", true}}},
		{"income into other_asset: balance up", "income", "50", "gold", "", []want{{"gold", "550.00", true}}},
		{"transfer asset->card (card payment): debt down", "transfer", "100", "pay", "card", []want{{"pay", "900.00", true}, {"card", "0.00", true}}},
		{"transfer overpays card: debt negative", "transfer", "150", "pay", "card", []want{{"pay", "850.00", true}, {"card", "-50.00", true}}},
		{"transfer card->asset (cash advance): debt up", "transfer", "50", "card", "pay", []want{{"card", "150.00", true}, {"pay", "1050.00", true}}},
		{"transfer loan->asset (draw loan): debt up", "transfer", "2000", "loan", "pay", []want{{"loan", "7000.00", true}, {"pay", "3000.00", true}}},
		{"transfer asset->asset", "transfer", "200", "pay", "gold", []want{{"pay", "800.00", true}, {"gold", "700.00", true}}},
		{"transfer asset->loan (repay): debt down", "transfer", "100", "gold", "loan", []want{{"gold", "400.00", true}, {"loan", "4900.00", true}}},
		{"transfer liability->liability (card pays loan)", "transfer", "10", "card", "loan", []want{{"card", "110.00", true}, {"loan", "4990.00", true}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			byName := map[string]balance.Balance{"pay": f.pay, "card": f.card, "gold": f.gold, "loan": f.loan}
			var to *balance.Balance
			if c.to != "" {
				b := byName[c.to]
				to = &b
			}
			e := f.create(f.tx(c.typ, c.amt, "THB", byName[c.from], to))
			if e.Version != 1 || !e.AdjustsBalances {
				t.Errorf("created: version %d adjusts %v", e.Version, e.AdjustsBalances)
			}
			touched := map[string]bool{}
			for _, w := range c.want {
				f.want(byName[w.name], w.amount, 2)
				touched[w.name] = true
			}
			initial := map[string]string{"pay": "1000.00", "card": "100.00", "gold": "500.00", "loan": "5000.00"}
			for name, amt := range initial {
				if !touched[name] {
					f.want(byName[name], amt, 1) // untouched: same amount, same version
				}
			}
			// Deleting reverses the effect exactly (version moves on).
			if err := f.st.Delete(context.Background(), e.ID); err != nil {
				t.Fatal(err)
			}
			for name, amt := range initial {
				v := int64(1)
				if touched[name] {
					v = 3
				}
				f.want(byName[name], amt, v)
			}
		})
	}
}

func TestOverLimitStillComputed(t *testing.T) {
	f := newFixture(t)
	f.create(f.tx("expense", "950", "THB", f.card, nil))
	got, _ := f.st.GetBalanceByUID(context.Background(), f.card.UID)
	if *got.Debt != "1050.00" || !*got.OverLimit || *got.Available != "-50.00" {
		t.Errorf("over limit: debt %s over %v avail %s", *got.Debt, *got.OverLimit, *got.Available)
	}
	f.create(f.tx("transfer", "1100", "THB", f.pay, &f.card))
	got, _ = f.st.GetBalanceByUID(context.Background(), f.card.UID)
	if *got.Debt != "-50.00" || *got.OverLimit || *got.Available != "1050.00" {
		t.Errorf("in credit: debt %s over %v avail %s", *got.Debt, *got.OverLimit, *got.Available)
	}
}

func TestUpdateReversesOldEffectAndAppliesNew(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	e := f.create(f.tx("expense", "30", "THB", f.pay, nil))
	f.want(f.pay, "970.00", 2)

	update := func(next transaction.Transaction) transaction.Transaction {
		t.Helper()
		got, err := f.st.Update(ctx, e.ID, func(transaction.Transaction) (transaction.Transaction, error) { return next, nil })
		if err != nil {
			t.Fatalf("update: %v", err)
		}
		return got
	}

	// Amount change: 1000 - 50.
	e = update(f.tx("expense", "50", "THB", f.pay, nil))
	f.want(f.pay, "950.00", 3)
	if e.Version != 2 {
		t.Errorf("transaction version = %d, want 2", e.Version)
	}
	// Type change: expense -> income: 1000 + 50.
	e = update(f.tx("income", "50", "THB", f.pay, nil))
	f.want(f.pay, "1050.00", 4)
	// Account change: income on pay -> expense on card.
	e = update(f.tx("expense", "50", "THB", f.card, nil))
	f.want(f.pay, "1000.00", 5)
	f.want(f.card, "150.00", 2)
	// Transfer pay -> card, then swap the destination to gold.
	e = update(f.tx("transfer", "80", "THB", f.pay, &f.card))
	f.want(f.pay, "920.00", 6)
	f.want(f.card, "20.00", 3)
	e = update(f.tx("transfer", "80", "THB", f.pay, &f.gold))
	f.want(f.pay, "920.00", 6) // net zero for pay: not written, version unchanged
	f.want(f.card, "100.00", 4)
	f.want(f.gold, "580.00", 2)
	// A note-only edit moves nothing and bumps no balance version.
	next := f.tx("transfer", "80", "THB", f.pay, &f.gold)
	next.Note = "moved"
	e = update(next)
	f.want(f.pay, "920.00", 6)
	f.want(f.gold, "580.00", 2)
	if e.Version != 7 {
		t.Errorf("transaction version = %d, want 7", e.Version)
	}
	// Delete: everything back to the start.
	if err := f.st.Delete(ctx, e.ID); err != nil {
		t.Fatal(err)
	}
	f.want(f.pay, "1000.00", 7)
	f.want(f.gold, "500.00", 3)
	f.want(f.card, "100.00", 4)
	f.want(f.loan, "5000.00", 1)
}

func fieldErr(t *testing.T, err error, field string) string {
	t.Helper()
	var ve *validate.ValidationError
	if !errors.As(err, &ve) || ve.Fields[field] == "" {
		t.Fatalf("want validation error on %s, got %v", field, err)
	}
	return ve.Fields[field]
}

func TestCurrencyMismatchRejected(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	usd := mustBalance(t, balance.Input{Name: "usd", Type: "payment_account", Currency: "USD", Balance: dec("10")})
	if err := f.st.CreateBalance(ctx, &usd); err != nil {
		t.Fatal(err)
	}

	// Create: USD expense on a THB account -> 422-style error, nothing written.
	e := f.tx("expense", "5", "USD", f.pay, nil)
	fieldErr(t, f.st.Create(ctx, &e), "balance_uid")
	// Transfer THB -> USD destination.
	e = f.tx("transfer", "5", "THB", f.pay, &usd)
	fieldErr(t, f.st.Create(ctx, &e), "to_balance_uid")
	if items, _ := f.st.List(ctx, ListFilter{}); len(items) != 0 {
		t.Errorf("rejected creates left %d rows", len(items))
	}
	f.want(f.pay, "1000.00", 1)
	f.want(usd, "10.00", 1)

	// Update to a mismatching currency is rejected; row and balance unchanged.
	ok := f.create(f.tx("expense", "5", "THB", f.pay, nil))
	_, err := f.st.Update(ctx, ok.ID, func(transaction.Transaction) (transaction.Transaction, error) {
		return f.tx("expense", "5", "USD", f.pay, nil), nil
	})
	fieldErr(t, err, "balance_uid")
	if got, _ := f.st.Get(ctx, ok.ID); got.Currency != "THB" || got.Version != 1 {
		t.Errorf("after rejected update: %+v", got)
	}
	f.want(f.pay, "995.00", 2)

	// A balance's currency cannot change while transactions reference it.
	_, err = f.st.UpdateBalance(ctx, f.pay.ID, func(cur balance.Balance) (balance.Balance, error) {
		in := cur.Input()
		in.Currency = "USD"
		return in.Validate()
	})
	if !errors.Is(err, ErrBalanceInUse) {
		t.Errorf("currency change in use: %v", err)
	}
	// ...but can when unreferenced.
	if _, err := f.st.UpdateBalance(ctx, f.gold.ID, func(cur balance.Balance) (balance.Balance, error) {
		in := cur.Input()
		in.Currency = "USD"
		return in.Validate()
	}); err != nil {
		t.Errorf("currency change unused: %v", err)
	}
}

func TestMissingBalance(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	e := f.create(f.tx("transfer", "10", "THB", f.pay, &f.gold))
	if err := f.st.DeleteBalance(ctx, f.gold.ID); err != nil {
		t.Fatal(err)
	}
	// Deleting the transaction reverses what it can (pay) and skips gold.
	if err := f.st.Delete(ctx, e.ID); err != nil {
		t.Fatalf("delete with missing balance: %v", err)
	}
	f.want(f.pay, "1000.00", 3)
	// A new transaction on a missing balance is rejected.
	n := f.tx("expense", "1", "THB", f.gold, nil)
	fieldErr(t, f.st.Create(ctx, &n), "balance_uid")
}

func TestVersionChecks(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	e := f.create(f.tx("expense", "10", "THB", f.pay, nil))
	same := func(cur transaction.Transaction) (transaction.Transaction, error) { return cur, nil }

	if _, err := f.st.UpdateWith(ctx, e.ID, WriteOptions{Version: 2}, same); !errors.Is(err, ErrVersionConflict) {
		t.Errorf("stale tx update: %v", err)
	}
	if _, err := f.st.UpdateWith(ctx, e.ID, WriteOptions{Version: 1}, same); err != nil {
		t.Errorf("fresh tx update: %v", err)
	}
	if _, err := f.st.UpdateWith(ctx, e.ID, WriteOptions{Version: 1}, same); !errors.Is(err, ErrVersionConflict) {
		t.Errorf("replayed tx update: %v", err)
	}
	if err := f.st.DeleteWith(ctx, e.ID, WriteOptions{Version: 1}); !errors.Is(err, ErrVersionConflict) {
		t.Errorf("stale tx delete: %v", err)
	}
	f.want(f.pay, "990.00", 2) // failed writes left the balance alone

	// Balances: version 2 now (moved by the transaction); 1 is stale.
	keep := func(cur balance.Balance) (balance.Balance, error) { return cur.Input().Validate() }
	if _, err := f.st.UpdateBalanceWith(ctx, f.pay.ID, WriteOptions{Version: 1}, keep); !errors.Is(err, ErrVersionConflict) {
		t.Errorf("stale balance update: %v", err)
	}
	if err := f.st.DeleteBalanceWith(ctx, f.pay.ID, WriteOptions{Version: 1}); !errors.Is(err, ErrVersionConflict) {
		t.Errorf("stale balance delete: %v", err)
	}
	b, err := f.st.UpdateBalanceWith(ctx, f.pay.ID, WriteOptions{Version: 2}, keep)
	if err != nil || b.Version != 3 {
		t.Errorf("fresh balance update: v%d %v", b.Version, err)
	}
	if err := f.st.DeleteBalanceWith(ctx, f.pay.ID, WriteOptions{Version: 3}); err != nil {
		t.Errorf("fresh balance delete: %v", err)
	}
	if err := f.st.DeleteBalanceWith(ctx, f.pay.ID, WriteOptions{Version: 3}); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete again: %v", err)
	}
}

// Manual PUT values override what transactions did; later transactions
// continue from the manual value.
func TestManualOverride(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.create(f.tx("expense", "30", "THB", f.card, nil)) // debt 130, v2
	got, err := f.st.UpdateBalanceWith(ctx, f.card.ID, WriteOptions{Version: 2}, func(cur balance.Balance) (balance.Balance, error) {
		in := cur.Input()
		in.Debt = dec("125")
		return in.ValidateUpdate(cur.Type)
	})
	if err != nil || *got.Debt != "125.00" || got.Version != 3 {
		t.Fatalf("manual debt: %+v %v", got, err)
	}
	f.want(f.card, "125.00", 3)
	f.create(f.tx("expense", "5", "THB", f.card, nil))
	f.want(f.card, "130.00", 4)
}

func TestConcurrentWritesStayConsistent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	const n = 40
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			typ, from, to := "expense", f.pay, (*balance.Balance)(nil)
			if i%2 == 1 {
				typ, to = "transfer", &f.card
			}
			e := f.tx(typ, "1", "THB", from, to)
			errs <- f.st.Create(ctx, &e)
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	f.want(f.pay, "960.00", 1+n)
	f.want(f.card, "80.00", 1+n/2)

	// Many clients PUT the same balance from the same version: exactly one wins.
	b, _ := f.st.GetBalanceByUID(ctx, f.gold.UID)
	var mu sync.Mutex
	wins, conflicts := 0, 0
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.st.UpdateBalanceWith(ctx, b.ID, WriteOptions{Version: b.Version}, func(cur balance.Balance) (balance.Balance, error) {
				in := cur.Input()
				in.Balance = dec("1")
				return in.Validate()
			})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
			case errors.Is(err, ErrVersionConflict):
				conflicts++
			default:
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if wins != 1 || conflicts != n-1 {
		t.Errorf("wins %d conflicts %d", wins, conflicts)
	}
}

// schemaV10 is a database as shipped at version 10: the v9 fixture (balances
// KBank debit THB 1000.00, UOB One SGD card debt 0 / limit 5000.00, Gold;
// two transactions) plus the user tables, and a deleted balance so the
// AUTOINCREMENT sequence is ahead of max(id).
const schemaV10 = schemaV9 + usersSchema + `
INSERT INTO balances (uid, name, type, currency, description, amount_scale, balance_minor, created_at)
  VALUES ('44444444-4444-4444-8444-444444444444', 'Gone', 'payment_account', 'THB', '', 2, 1, '2026-10-07T00:00:00Z');
DELETE FROM balances WHERE name = 'Gone';
PRAGMA user_version = 10;
`

func TestMigrationV10ToCurrent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v10.db")
	rawDB(t, path, schemaV10)
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open v10: %v", err)
	}
	// Existing balances: same ids, uids and amounts (no recompute), version 1.
	all, err := st.ListBalances(ctx, "")
	if err != nil || len(all) != 3 {
		t.Fatalf("balances: %v %v", all, err)
	}
	for _, b := range all {
		if b.Version != 1 {
			t.Errorf("%s version %d", b.Name, b.Version)
		}
	}
	kbank, _ := st.GetBalanceByUID(ctx, "11111111-1111-4111-8111-111111111111")
	if kbank.ID != 1 || *kbank.Balance != "1000.00" || kbank.UpdatedAt != nil {
		t.Errorf("kbank: %+v", kbank)
	}
	// Existing transactions: version 1, never applied to balances.
	txs, err := st.List(ctx, ListFilter{})
	if err != nil || len(txs) != 2 {
		t.Fatalf("transactions: %v %v", txs, err)
	}
	for _, e := range txs {
		if e.Version != 1 || e.AdjustsBalances {
			t.Errorf("legacy transaction %s: version %d adjusts %v", e.UID, e.Version, e.AdjustsBalances)
		}
	}
	// Editing / deleting a legacy transaction leaves balances alone.
	var thb transaction.Transaction
	for _, e := range txs {
		if e.Currency == "THB" {
			thb = e
		}
	}
	upd, err := st.Update(ctx, thb.ID, func(cur transaction.Transaction) (transaction.Transaction, error) {
		cur.AmountMinor, cur.Amount = 99999, "999.99"
		return cur, nil
	})
	if err != nil || upd.Version != 2 || upd.AdjustsBalances {
		t.Errorf("legacy update: %+v %v", upd, err)
	}
	if err := st.Delete(ctx, thb.ID); err != nil {
		t.Fatal(err)
	}
	if b, _ := st.GetBalanceByUID(ctx, kbank.UID); *b.Balance != "1000.00" || b.Version != 1 {
		t.Errorf("legacy edits moved the balance: %s v%d", *b.Balance, b.Version)
	}
	// New balances do not reuse the id of the deleted one.
	nb := mustBalance(t, balance.Input{Name: "New", Type: "payment_account", Currency: "THB", Balance: dec("1")})
	if err := st.CreateBalance(ctx, &nb); err != nil || nb.ID != 5 {
		t.Errorf("new balance id = %d (%v), want 5", nb.ID, err)
	}
	// Negative debt is now allowed by the table.
	card, _ := st.GetBalanceByUID(ctx, "22222222-2222-4222-8222-222222222222")
	if _, err := st.UpdateBalance(ctx, card.ID, func(cur balance.Balance) (balance.Balance, error) {
		in := cur.Input()
		in.Debt = dec("-12.34")
		return in.Validate()
	}); err != nil {
		t.Errorf("negative debt after migration: %v", err)
	}
	st.Close()
	assertCurrentSchema(t, path)

	// Idempotent: running the v11-v13 migrations again changes nothing.
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := addVersions(tx); err != nil {
		t.Fatalf("re-run v11: %v", err)
	}
	if err := dropBalanceAdjustments(tx); err != nil {
		t.Fatalf("re-run v12: %v", err)
	}
	if err := rebuildTransactionsV13(tx); err != nil {
		t.Fatalf("re-run v13: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var debt, version int64
	if err := db.QueryRow(`SELECT debt_minor, version FROM balances WHERE uid = ?`, card.UID).Scan(&debt, &version); err != nil || debt != -1234 || version != 2 {
		t.Errorf("after re-run: debt %d version %d %v", debt, version, err)
	}
	db.Close()
	assertCurrentSchema(t, path)
}

// A crash between the balances rebuild and the version bump cannot happen
// (one SQL transaction), but a v10 database whose balances table already has
// a version column (e.g. edited by hand) is still rebuilt to drop the debt
// CHECK, keeping its versions.
func TestMigrationV11KeepsExistingVersionColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v10v.db")
	rawDB(t, path, schemaV10+`
ALTER TABLE balances ADD COLUMN version INTEGER NOT NULL DEFAULT 1;
UPDATE balances SET version = 7 WHERE name = 'Gold';
`)
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	gold, err := st.GetBalanceByUID(context.Background(), "33333333-3333-4333-8333-333333333333")
	if err != nil || gold.Version != 7 {
		t.Errorf("gold: v%d %v", gold.Version, err)
	}
	st.Close()
	assertCurrentSchema(t, path)
}

// preReleaseAdjustmentsSchema is the balance_adjustments table that a
// pre-release build of v11 created (removed before release; v12 drops it).
const preReleaseAdjustmentsSchema = `
CREATE TABLE balance_adjustments (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    uid          TEXT    NOT NULL UNIQUE CHECK (length(uid) = 36),
    balance_uid  TEXT    NOT NULL CHECK (length(balance_uid) = 36),
    field        TEXT    NOT NULL CHECK (field IN ('balance', 'debt', 'limit')),
    old_minor    INTEGER NOT NULL,
    old_scale    INTEGER NOT NULL CHECK (old_scale BETWEEN 0 AND 4),
    old_currency TEXT    NOT NULL CHECK (length(old_currency) = 3),
    new_minor    INTEGER NOT NULL,
    new_scale    INTEGER NOT NULL CHECK (new_scale BETWEEN 0 AND 4),
    new_currency TEXT    NOT NULL CHECK (length(new_currency) = 3),
    note         TEXT    NOT NULL DEFAULT '',
    version      INTEGER NOT NULL,
    created_at   TEXT    NOT NULL
);
CREATE INDEX idx_balance_adjustments_balance_uid ON balance_adjustments (balance_uid, id);
INSERT INTO balance_adjustments (uid, balance_uid, field, old_minor, old_scale, old_currency, new_minor, new_scale, new_currency, note, version, created_at)
  VALUES ('55555555-5555-4555-8555-555555555555', '11111111-1111-4111-8111-111111111111', 'balance', 100000, 2, 'THB', 90000, 2, 'THB', 'x', 2, '2026-10-08T00:00:00Z');
PRAGMA user_version = 11;
`

// A database migrated by the pre-release v11 (with balance_adjustments) is
// upgraded to v12: the table, its index and its sequence row are dropped;
// balances are untouched.
func TestMigrationV12DropsBalanceAdjustments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v11pre.db")
	rawDB(t, path, schemaV10)
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := addVersions(tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(preReleaseAdjustmentsSchema); err != nil {
		t.Fatal(err)
	}
	db.Close()

	st, err := Open(path)
	if err != nil {
		t.Fatalf("open pre-release v11: %v", err)
	}
	kbank, err := st.GetBalanceByUID(context.Background(), "11111111-1111-4111-8111-111111111111")
	if err != nil || *kbank.Balance != "1000.00" || kbank.Version != 1 {
		t.Errorf("kbank: %+v %v", kbank, err)
	}
	st.Close()
	assertCurrentSchema(t, path)

	db, err = sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name LIKE '%balance_adjustments%'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("balance_adjustments objects left: %d %v", n, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_sequence WHERE name = 'balance_adjustments'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("sequence row left: %d %v", n, err)
	}
}
