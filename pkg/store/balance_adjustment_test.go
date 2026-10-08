package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nutmos/expensetrackr/pkg/balance"
	"github.com/nutmos/expensetrackr/pkg/transaction"
)

// fixClock pins the balance adjustment clock to 2026-10-08 21:30:00 +08:00.
func fixClock(t *testing.T) {
	t.Helper()
	at := time.Date(2026, 10, 8, 21, 30, 0, 123, time.FixedZone("SGT", 8*3600))
	old := clock
	clock = func() time.Time { return at }
	t.Cleanup(func() { clock = old })
}

// edit runs a manual balance edit (the PUT/PATCH path).
func (f *fixture) edit(b balance.Balance, opts WriteOptions, mut func(*balance.Input)) (balance.Balance, error) {
	f.t.Helper()
	return f.st.UpdateBalanceWith(context.Background(), b.ID, opts, func(cur balance.Balance) (balance.Balance, error) {
		in := cur.Input()
		mut(&in)
		return in.ValidateUpdate(cur.Type)
	})
}

func (f *fixture) adjustments() []transaction.Transaction {
	f.t.Helper()
	got, err := f.st.List(context.Background(), ListFilter{Type: transaction.BalanceAdjustment})
	if err != nil {
		f.t.Fatal(err)
	}
	return got
}

func TestManualEditRecordsAdjustment(t *testing.T) {
	fixClock(t)
	f := newFixture(t)
	cases := []struct {
		b      balance.Balance
		mut    func(*balance.Input)
		amount string
		dir    transaction.Direction
		note   string
		after  string
	}{
		{f.pay, func(in *balance.Input) { in.Balance = dec("950") }, "50.00", transaction.Decrease, "Manual edit of balance: 1000.00 → 950.00", "950.00"},
		{f.gold, func(in *balance.Input) { in.Balance = dec("750.5") }, "250.50", transaction.Increase, "Manual edit of balance: 500.00 → 750.50", "750.50"},
		{f.card, func(in *balance.Input) { in.Debt = dec("175") }, "75.00", transaction.Increase, "Manual edit of debt: 100.00 → 175.00", "175.00"},
		{f.loan, func(in *balance.Input) { in.Debt = dec("-20") }, "5020.00", transaction.Decrease, "Manual edit of debt: 5000.00 → -20.00", "-20.00"},
	}
	for _, c := range cases {
		if _, err := f.edit(c.b, WriteOptions{Version: 1}, c.mut); err != nil {
			t.Fatalf("%s: %v", c.b.Name, err)
		}
		// The edit sets the value exactly once: the adjustment is not applied
		// on top of it (no double counting) and the version moves by one.
		f.want(c.b, c.after, 2)
		var got []transaction.Transaction
		for _, a := range f.adjustments() {
			if a.BalanceUID == c.b.UID {
				got = append(got, a)
			}
		}
		if len(got) != 1 {
			t.Fatalf("%s: %d adjustments, want 1", c.b.Name, len(got))
		}
		a := got[0]
		if a.Type != transaction.BalanceAdjustment || a.Amount != c.amount || a.AdjustmentDirection == nil || *a.AdjustmentDirection != c.dir ||
			a.Currency != "THB" || a.Account != c.b.Name || a.Note != c.note || a.SpentAt != "2026-10-08T21:30:00+08:00" ||
			a.CategoryUID != nil || a.ToBalanceUID != nil || a.AdjustsBalances || a.Version != 1 || !looksLikeUUID(a.UID) {
			t.Errorf("%s adjustment: %+v (dir %v)", c.b.Name, a, a.AdjustmentDirection)
		}
		if len(a.Effects()) != 0 {
			t.Errorf("%s: adjustment has effects %v", c.b.Name, a.Effects())
		}
	}
	if n := len(f.adjustments()); n != 4 {
		t.Errorf("%d adjustments in total", n)
	}
	// Ordinary transactions are not affected and do not show up in the filter.
	f.create(f.tx("expense", "10", "THB", f.pay, nil))
	f.want(f.pay, "940.00", 3)
	all, _ := f.st.List(context.Background(), ListFilter{})
	if len(all) != 5 || len(f.adjustments()) != 4 {
		t.Errorf("list %d, adjustments %d", len(all), len(f.adjustments()))
	}
	exp, _ := f.st.List(context.Background(), ListFilter{Type: transaction.Expense})
	if len(exp) != 1 {
		t.Errorf("expense filter: %d", len(exp))
	}
	// Deleting the expense reverses only the expense.
	if err := f.st.Delete(context.Background(), exp[0].ID); err != nil {
		t.Fatal(err)
	}
	f.want(f.pay, "950.00", 4)
}

func TestNoAdjustmentWithoutAmountChange(t *testing.T) {
	f := newFixture(t)
	steps := []struct {
		name string
		b    balance.Balance
		mut  func(*balance.Input)
	}{
		{"rename", f.pay, func(in *balance.Input) { in.Name = "Pay"; in.Description = "main" }},
		{"same value re-sent", f.pay, func(in *balance.Input) { in.Balance = dec("1000.00") }},
		{"limit only", f.card, func(in *balance.Input) { in.Limit = dec("2000") }},
		{"limit only (loan)", f.loan, func(in *balance.Input) { in.Limit = dec("1") }},
		// Currency change (possible only while nothing references the balance):
		// the amounts are in different currencies, so no adjustment.
		{"currency change", f.gold, func(in *balance.Input) { in.Currency = "USD"; in.Balance = dec("600") }},
	}
	for _, s := range steps {
		if _, err := f.edit(s.b, WriteOptions{}, s.mut); err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		if n := len(f.adjustments()); n != 0 {
			t.Fatalf("%s: %d adjustments, want 0", s.name, n)
		}
	}
	// Debt and limit in one edit: exactly one adjustment, for the debt.
	if _, err := f.edit(f.card, WriteOptions{}, func(in *balance.Input) { in.Debt = dec("90"); in.Limit = dec("3000") }); err != nil {
		t.Fatal(err)
	}
	if a := f.adjustments(); len(a) != 1 || a[0].Amount != "10.00" || *a[0].AdjustmentDirection != transaction.Decrease {
		t.Errorf("debt+limit edit: %+v", a)
	}
	// Once an adjustment references the balance, its currency is fixed.
	if _, err := f.edit(f.card, WriteOptions{}, func(in *balance.Input) { in.Currency = "USD" }); !errors.Is(err, ErrBalanceInUse) {
		t.Errorf("currency change after adjustment: %v", err)
	}
}

func TestStaleEditRecordsNothing(t *testing.T) {
	f := newFixture(t)
	f.create(f.tx("expense", "1", "THB", f.pay, nil)) // pay v2
	if _, err := f.edit(f.pay, WriteOptions{Version: 1}, func(in *balance.Input) { in.Balance = dec("5") }); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale edit: %v", err)
	}
	// A rejected edit (validation error) records nothing either.
	if _, err := f.edit(f.pay, WriteOptions{Version: 2}, func(in *balance.Input) { in.Balance = dec("x") }); err == nil {
		t.Fatal("invalid edit accepted")
	}
	if n := len(f.adjustments()); n != 0 {
		t.Errorf("%d adjustments after rejected edits", n)
	}
	f.want(f.pay, "999.00", 2)
}

func TestAdjustmentIsReadOnly(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.edit(f.pay, WriteOptions{}, func(in *balance.Input) { in.Balance = dec("900") }); err != nil {
		t.Fatal(err)
	}
	adj := f.adjustments()[0]
	if _, err := f.st.UpdateWith(ctx, adj.ID, WriteOptions{Version: 1}, func(cur transaction.Transaction) (transaction.Transaction, error) {
		cur.Note = "x"
		return cur, nil
	}); !errors.Is(err, ErrReadOnly) {
		t.Errorf("update adjustment: %v", err)
	}
	if err := f.st.DeleteWith(ctx, adj.ID, WriteOptions{}); !errors.Is(err, ErrReadOnly) {
		t.Errorf("delete adjustment: %v", err)
	}
	// Neither can one be created, nor an ordinary transaction turned into one.
	e := f.tx("expense", "5", "THB", f.pay, nil)
	e.Type = transaction.BalanceAdjustment
	if err := f.st.Create(ctx, &e); !errors.Is(err, ErrReadOnly) {
		t.Errorf("create adjustment: %v", err)
	}
	exp := f.create(f.tx("expense", "5", "THB", f.pay, nil))
	if _, err := f.st.Update(ctx, exp.ID, func(cur transaction.Transaction) (transaction.Transaction, error) {
		cur.Type = transaction.BalanceAdjustment
		return cur, nil
	}); !errors.Is(err, ErrReadOnly) {
		t.Errorf("update to adjustment: %v", err)
	}
	if got, _ := f.st.Get(ctx, adj.ID); got.Version != 1 || got.Note != adj.Note {
		t.Errorf("adjustment changed: %+v", got)
	}
	f.want(f.pay, "895.00", 3)

	// The table itself refuses adjustment rows that would move a balance or
	// carry a destination/category, and a direction on other types.
	for _, q := range []string{
		`INSERT INTO transactions (uid, type, amount_minor, amount_scale, currency, balance_uid, account, spent_at, spent_at_unix, created_at, balance_applied, adjustment_direction)
		 VALUES ('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa1', 'balance_adjustment', 1, 2, 'THB', '` + f.pay.UID + `', 'pay', 'x', 0, 'x', 1, 'increase')`,
		`INSERT INTO transactions (uid, type, amount_minor, amount_scale, currency, balance_uid, account, spent_at, spent_at_unix, created_at, adjustment_direction)
		 VALUES ('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa2', 'balance_adjustment', 1, 2, 'THB', '` + f.pay.UID + `', 'pay', 'x', 0, 'x', NULL)`,
		`INSERT INTO transactions (uid, type, amount_minor, amount_scale, currency, balance_uid, account, spent_at, spent_at_unix, created_at, adjustment_direction)
		 VALUES ('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa3', 'expense', 1, 2, 'THB', '` + f.pay.UID + `', 'pay', 'x', 0, 'x', 'increase')`,
		`INSERT INTO transactions (uid, type, amount_minor, amount_scale, currency, balance_uid, account, spent_at, spent_at_unix, created_at, adjustment_direction, to_balance_uid)
		 VALUES ('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa4', 'balance_adjustment', 1, 2, 'THB', '` + f.pay.UID + `', 'pay', 'x', 0, 'x', 'increase', '` + f.card.UID + `')`,
		`INSERT INTO transactions (uid, type, amount_minor, amount_scale, currency, balance_uid, account, spent_at, spent_at_unix, created_at, adjustment_direction)
		 VALUES ('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa5', 'balance_adjustment', 1, 2, 'THB', '` + f.pay.UID + `', 'pay', 'x', 0, 'x', 'sideways')`,
	} {
		if _, err := f.st.db.Exec(q); err == nil || !strings.Contains(err.Error(), "CHECK") {
			t.Errorf("CHECK did not reject: %v\n%s", err, q)
		}
	}
}

// schemaV12 builds a v12 database from the v10 fixture with v11-era rows: an
// applied, edited expense, a transfer, and a deleted row (so the
// AUTOINCREMENT counter is ahead of max(id)).
func schemaV12(t *testing.T, path string) {
	t.Helper()
	rawDB(t, path, schemaV10)
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
		t.Fatal(err)
	}
	if err := dropBalanceAdjustments(tx); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`UPDATE transactions SET version = 3, updated_at = '2026-10-08T01:00:00Z', note = 'edited' WHERE id = 1`,
		`INSERT INTO transactions (uid, type, amount_minor, amount_scale, currency, balance_uid, account, to_balance_uid, to_account, spent_at, spent_at_unix, note, created_at, version, balance_applied)
		 VALUES ('66666666-6666-4666-8666-666666666666', 'transfer', 2500, 2, 'THB', '11111111-1111-4111-8111-111111111111', 'KBank debit',
		         '33333333-3333-4333-8333-333333333333', 'Gold', '2026-10-08T09:00:00+08:00', 1791421200, '', '2026-10-08T01:00:00Z', 1, 1)`,
		`INSERT INTO transactions (uid, type, amount_minor, amount_scale, currency, balance_uid, account, spent_at, spent_at_unix, note, created_at, version, balance_applied)
		 VALUES ('77777777-7777-4777-8777-777777777777', 'income', 100, 2, 'THB', '11111111-1111-4111-8111-111111111111', 'KBank debit',
		         '2026-10-08T10:00:00+08:00', 1791424800, 'gone', '2026-10-08T02:00:00Z', 1, 1)`,
		`DELETE FROM transactions WHERE uid = '77777777-7777-4777-8777-777777777777'`,
		`PRAGMA user_version = 12`,
	} {
		if _, err := tx.Exec(q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// dumpTransactions returns every transactions row (v12 columns) as text.
func dumpTransactions(t *testing.T, path string) []string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT id, uid, amount_minor, amount_scale, currency, balance_uid, account, type, to_balance_uid, to_account,
	       category_uid, spent_at, spent_at_unix, note, created_at, updated_at, version, balance_applied FROM transactions ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		v := make([]any, 18)
		p := make([]any, len(v))
		for i := range v {
			p[i] = &v[i]
		}
		if err := rows.Scan(p...); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprint(v...))
	}
	return out
}

func sequenceOf(t *testing.T, path, table string) int64 {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var seq int64
	if err := db.QueryRow(`SELECT seq FROM sqlite_sequence WHERE name = ?`, table).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	return seq
}

func TestMigrationV12ToV13(t *testing.T) {
	fixClock(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v12.db")
	schemaV12(t, path)
	before := dumpTransactions(t, path)
	seq := sequenceOf(t, path, "transactions")
	if len(before) != 3 || seq != 4 {
		t.Fatalf("fixture: %d rows, seq %d", len(before), seq)
	}

	st, err := Open(path)
	if err != nil {
		t.Fatalf("open v12: %v", err)
	}
	// Rows, ids, uids and values are unchanged; direction is null.
	st.Close()
	if after := dumpTransactions(t, path); strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Errorf("rows changed:\n got %v\nwant %v", after, before)
	}
	if s := sequenceOf(t, path, "transactions"); s != seq {
		t.Errorf("sequence %d, want %d", s, seq)
	}
	assertCurrentSchema(t, path)

	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	all, err := st.List(ctx, ListFilter{})
	if err != nil || len(all) != 3 {
		t.Fatalf("list: %v %v", all, err)
	}
	for _, e := range all {
		if e.AdjustmentDirection != nil {
			t.Errorf("%s: direction %v", e.UID, *e.AdjustmentDirection)
		}
	}
	// uid stays unique.
	if _, err := st.db.Exec(`INSERT INTO transactions (uid, amount_minor, amount_scale, currency, balance_uid, account, spent_at, spent_at_unix, created_at)
	    SELECT uid, 1, 2, 'THB', balance_uid, account, 'x', 0, 'x' FROM transactions WHERE id = 1`); err == nil {
		t.Error("duplicate uid accepted")
	}
	// New rows do not reuse the deleted id, and the new type works.
	kbank, _ := st.GetBalanceByUID(ctx, "11111111-1111-4111-8111-111111111111")
	if _, err := st.UpdateBalanceWith(ctx, kbank.ID, WriteOptions{Version: kbank.Version}, func(cur balance.Balance) (balance.Balance, error) {
		in := cur.Input()
		in.Balance = dec("1234.56")
		return in.ValidateUpdate(cur.Type)
	}); err != nil {
		t.Fatal(err)
	}
	adj, err := st.List(ctx, ListFilter{Type: transaction.BalanceAdjustment})
	if err != nil || len(adj) != 1 || adj[0].ID != 5 || adj[0].Amount != "234.56" || *adj[0].AdjustmentDirection != transaction.Increase {
		t.Errorf("adjustment after migration: %+v %v", adj, err)
	}
	st.Close()

	// Idempotent: re-running v13 on a v13 database changes nothing.
	before = dumpTransactions(t, path)
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := rebuildTransactionsV13(tx); err != nil {
		t.Fatalf("re-run v13: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if after := dumpTransactions(t, path); strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Errorf("re-run changed rows")
	}
	assertCurrentSchema(t, path)
}
