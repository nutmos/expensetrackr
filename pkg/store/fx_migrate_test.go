package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nutmos/expensetrackr/pkg/balance"
	"github.com/nutmos/expensetrackr/pkg/transaction"
)

// v16TransactionsTable is transactionsTable without the v17 columns.
func v16TransactionsTable(name string) string {
	var out []string
	for _, l := range strings.Split(transactionsTable(name), "\n") {
		skip := false
		for _, c := range fxCols {
			if strings.HasPrefix(strings.TrimSpace(l), c[0]+" ") {
				skip = true
			}
		}
		if !skip {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// TestMigrationV16ToV17 checks every row, balance and the id sequence
// survive, existing rows get NULL cross-currency fields, the fx cache tables
// exist, and the migration is idempotent.
func TestMigrationV16ToV17(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v16.db")
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
	cols := "id, uid, amount_minor, amount_scale, currency, balance_uid, account, type, to_balance_uid, to_account, category_uid, spent_at, spent_at_unix, note, created_at, updated_at, version, balance_applied, adjustment_direction"
	for _, q := range []string{
		v16TransactionsTable("transactions_old"),
		`INSERT INTO transactions_old (` + cols + `) SELECT ` + cols + ` FROM transactions`,
		`DROP TABLE transactions`,
		`ALTER TABLE transactions_old RENAME TO transactions`,
		transactionsIndexes,
		`DELETE FROM sqlite_sequence WHERE name = 'transactions_old'`,
		`INSERT OR REPLACE INTO sqlite_sequence (name, seq) VALUES ('transactions', 3)`,
		`DROP TABLE fx_rates`, `DROP TABLE fx_rate_lookups`,
		`PRAGMA user_version = 16`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	var balancesBefore string
	if err := db.QueryRow(`SELECT group_concat(uid || ':' || coalesce(balance_minor, '') || ':' || coalesce(debt_minor, '') || ':' || version, ',') FROM balances`).Scan(&balancesBefore); err != nil {
		t.Fatal(err)
	}
	db.Close()
	before := dumpTransactions(t, path)
	if len(before) != 3 {
		t.Fatalf("fixture %v", before)
	}

	st, err := Open(path)
	if err != nil {
		t.Fatalf("open v16: %v", err)
	}
	st.Close()
	if after := dumpTransactions(t, path); strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Errorf("rows changed:\n got %v\nwant %v", after, before)
	}
	assertCurrentSchema(t, path)
	db, _ = sql.Open("sqlite", "file:"+path)
	defer db.Close()
	var balancesAfter string
	if err := db.QueryRow(`SELECT group_concat(uid || ':' || coalesce(balance_minor, '') || ':' || coalesce(debt_minor, '') || ':' || version, ',') FROM balances`).Scan(&balancesAfter); err != nil {
		t.Fatal(err)
	}
	if balancesAfter != balancesBefore {
		t.Errorf("balances changed: %s -> %s", balancesBefore, balancesAfter)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM transactions WHERE to_amount_minor IS NULL AND to_currency IS NULL AND exchange_rate IS NULL AND rate_source IS NULL AND rate_date IS NULL`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("rows with null fx fields %d, want 3", n)
	}
	// Idempotent re-run.
	tx, _ := db.Begin()
	if err := addCrossCurrency(tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if after := dumpTransactions(t, path); strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Errorf("re-run changed rows")
	}

	// The migrated database stores a cross-currency transfer and the cache.
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	usd := mustBalance(t, balance.Input{Name: "usd", Type: "payment_account", Currency: "USD", Balance: dec("100")})
	if err := st.CreateBalance(ctx, &usd); err != nil {
		t.Fatal(err)
	}
	e := transaction.Transaction{Type: transaction.Transfer, Amount: "10.00", AmountMinor: 1000, Currency: "USD", BalanceUID: usd.UID, Account: "usd",
		ToBalanceUID: &f.pay.UID, ToAccount: &f.pay.Name, SpentAt: "2026-10-09T09:00:00Z", SpentTime: time.Date(2026, 10, 9, 9, 0, 0, 0, time.UTC)}
	m, a, c, r, s, d := int64(33615), "336.15", "THB", "33.615", "frankfurter", "2026-10-09"
	e.ToAmountMinor, e.ToAmount, e.ToCurrency, e.ExchangeRate, e.RateSource, e.RateDate = &m, &a, &c, &r, &s, &d
	if err := st.Create(ctx, &e); err != nil {
		t.Fatal(err)
	}
	if e.ID != 4 {
		t.Errorf("id %d, want 4", e.ID)
	}
	got, _ := st.Get(ctx, e.ID)
	if *got.ToAmount != "336.15" || *got.RateDate != d || *got.ExchangeRate != r {
		t.Errorf("stored %+v", got)
	}
	if err := st.FXStore(ctx, "USD", "THB", "2026-10-10", "2026-10-09", "33.615", time.Now()); err != nil {
		t.Fatal(err)
	}
	if rate, rd, _, ok, err := st.FXLookup(ctx, "USD", "THB", "2026-10-10"); !ok || err != nil || rate != "33.615" || rd != "2026-10-09" {
		t.Errorf("cache: %s %s %v %v", rate, rd, ok, err)
	}
}
