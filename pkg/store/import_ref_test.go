package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nutmos/expensetrackr/pkg/transaction"
)

// TestMigrationV17ToV18 builds a v17 database (transactions without
// import_ref, no unique index), migrates it and checks every row and balance
// survive, existing rows get NULL import_ref, the partial unique index works
// and the migration is idempotent.
func TestMigrationV17ToV18(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v17.db")
	f := newFixture(t)
	f.create(f.tx("expense", "10", "THB", f.pay, nil))
	f.create(f.tx("transfer", "30", "THB", f.pay, &f.card))
	f.create(f.tx("asset_purchase", "40", "THB", f.pay, &f.gold))
	if _, err := f.st.db.Exec(`VACUUM INTO ?`, path); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	var v17 []string
	for _, l := range strings.Split(transactionsTable("transactions_old"), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(l), "import_ref ") {
			v17 = append(v17, l)
		}
	}
	cols := "id, uid, amount_minor, amount_scale, currency, balance_uid, account, type, to_balance_uid, to_account, category_uid, spent_at, spent_at_unix, note, created_at, updated_at, version, balance_applied, adjustment_direction, to_amount_minor, to_amount_scale, to_currency, exchange_rate, rate_source, rate_date"
	for _, q := range []string{
		strings.Join(v17, "\n"),
		`INSERT INTO transactions_old (` + cols + `) SELECT ` + cols + ` FROM transactions`,
		`DROP TABLE transactions`,
		`ALTER TABLE transactions_old RENAME TO transactions`,
		transactionsIndexes,
		`DELETE FROM sqlite_sequence WHERE name = 'transactions_old'`,
		`INSERT OR REPLACE INTO sqlite_sequence (name, seq) VALUES ('transactions', 3)`,
		`PRAGMA user_version = 17`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if ok, _ := columnExistsDB(db, "import_ref"); ok {
		t.Fatal("fixture still has import_ref")
	}
	var balancesBefore string
	db.QueryRow(`SELECT group_concat(uid || ':' || coalesce(balance_minor, '') || ':' || coalesce(debt_minor, '') || ':' || version, ',') FROM balances`).Scan(&balancesBefore)
	db.Close()
	before := dumpTransactions(t, path)

	st, err := Open(path)
	if err != nil {
		t.Fatalf("open v17: %v", err)
	}
	st.Close()
	if after := dumpTransactions(t, path); strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Errorf("rows changed:\n got %v\nwant %v", after, before)
	}
	assertCurrentSchema(t, path)
	db, _ = sql.Open("sqlite", "file:"+path)
	defer db.Close()
	var balancesAfter string
	db.QueryRow(`SELECT group_concat(uid || ':' || coalesce(balance_minor, '') || ':' || coalesce(debt_minor, '') || ':' || version, ',') FROM balances`).Scan(&balancesAfter)
	if balancesAfter != balancesBefore {
		t.Errorf("balances changed: %s -> %s", balancesBefore, balancesAfter)
	}
	var n int
	db.QueryRow(`SELECT count(*) FROM transactions WHERE import_ref IS NULL`).Scan(&n)
	if n != 3 {
		t.Errorf("rows with null import_ref %d, want 3", n)
	}
	tx, _ := db.Begin()
	if err := addImportRef(tx); err != nil {
		t.Fatalf("re-run: %v", err)
	}
	tx.Commit()

	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ref := "bank-ref-1"
	e := f.tx("expense", "5", "THB", f.pay, nil)
	e.ImportRef = &ref
	if err := st.Create(ctx, &e); err != nil {
		t.Fatal(err)
	}
	dup := f.tx("expense", "6", "THB", f.pay, nil)
	dup.ImportRef = &ref
	var de *DuplicateImportRefError
	if err := st.Create(ctx, &dup); !errors.As(err, &de) || de.ExistingUID != e.UID {
		t.Errorf("duplicate: %v", err)
	}
	other := f.tx("expense", "6", "THB", f.card, nil)
	other.ImportRef = &ref
	if err := st.Create(ctx, &other); err != nil {
		t.Errorf("same ref on another balance: %v", err)
	}
}

func columnExistsDB(db *sql.DB, col string) (bool, error) {
	var n int
	err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('transactions') WHERE name = ?`, col).Scan(&n)
	return n > 0, err
}

// TestCreateBulkStore covers the store layer directly: savepoint rollback of a
// failing item, in-batch duplicates, atomic rollback and dry run.
func TestCreateBulkStore(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	ref := func(e transaction.Transaction, r string) transaction.Transaction { e.ImportRef = &r; return e }
	bad := f.tx("expense", "1", "USD", f.pay, nil) // currency mismatch, detected in the tx
	items := []BulkItem{
		{Tx: ref(f.tx("expense", "10", "THB", f.pay, nil), "a")},
		{Tx: bad},
		{Tx: ref(f.tx("expense", "11", "THB", f.pay, nil), "a")},
		{Err: errors.New("pre-validation failed")},
	}
	payBefore, _ := f.amount(f.pay)
	out, err := f.st.CreateBulk(ctx, items, BulkOptions{Atomic: false})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range out.Results {
		got = append(got, string(r.Status))
	}
	if strings.Join(got, ",") != "created,failed,skipped,failed" || !out.Committed {
		t.Fatalf("statuses %v committed %v", got, out.Committed)
	}
	if out.Results[2].ExistingUID != out.Results[0].Tx.UID {
		t.Errorf("existing uid")
	}
	payAfter, _ := f.amount(f.pay)
	if payBefore == payAfter {
		t.Errorf("balance did not move")
	}
	// Atomic with a failure and dry run write nothing.
	for _, opts := range []BulkOptions{{Atomic: true}, {Atomic: false, DryRun: true}} {
		out, err := f.st.CreateBulk(ctx, []BulkItem{{Tx: f.tx("expense", "7", "THB", f.pay, nil)}, {Tx: bad}}, opts)
		if err != nil || out.Committed {
			t.Errorf("%+v: committed %v err %v", opts, out.Committed, err)
		}
		if now, _ := f.amount(f.pay); now != payAfter {
			t.Errorf("%+v: balance moved %s -> %s", opts, payAfter, now)
		}
	}
	list, _ := f.st.List(ctx, ListFilter{})
	if len(list) != 1 {
		t.Errorf("rows %d, want 1", len(list))
	}
}
