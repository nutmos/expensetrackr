package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"expense-service/internal/balance"
	"expense-service/internal/money"
)

// schemaV2 is the v2 schema (table "transactions", no balances) with rows,
// including a deleted id so the AUTOINCREMENT counter is ahead of max(id).
const schemaV2 = `
CREATE TABLE transactions (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    amount_minor  INTEGER NOT NULL CHECK (amount_minor > 0),
    amount_scale  INTEGER NOT NULL CHECK (amount_scale BETWEEN 0 AND 4),
    currency      TEXT    NOT NULL CHECK (length(currency) = 3),
    account       TEXT    NOT NULL CHECK (length(account) > 0),
    spent_at      TEXT    NOT NULL,
    spent_at_unix INTEGER NOT NULL,
    note          TEXT    NOT NULL DEFAULT '',
    created_at    TEXT    NOT NULL,
    updated_at    TEXT
);
CREATE INDEX idx_transactions_spent_at_unix ON transactions (spent_at_unix DESC, id DESC);
INSERT INTO transactions (amount_minor, amount_scale, currency, account, spent_at, spent_at_unix, note, created_at, updated_at) VALUES
  (12050, 2, 'THB', 'KBank debit', '2026-10-05T09:00:00+07:00', 1791172800, 'pad thai', '2026-10-06T13:00:00Z', NULL),
  (1890,  2, 'SGD', 'Cash',        '2026-10-06T12:30:00+08:00', 1791267000, 'chicken rice', '2026-10-06T13:01:00Z', '2026-10-06T13:05:00Z'),
  (100,   2, 'USD', 'Card',        '2026-10-06T00:00:00Z',      1791244800, 'to delete', '2026-10-06T13:02:00Z', NULL);
DELETE FROM transactions WHERE id = 3;
PRAGMA user_version = 2;
`

func mustBalance(t *testing.T, in balance.Input) balance.Balance {
	t.Helper()
	b, err := in.Validate()
	if err != nil {
		t.Fatalf("validate %+v: %v", in, err)
	}
	return b
}

func dec(s string) *money.DecimalInput { d := money.DecimalInput(s); return &d }

func TestMigrationV2ToV3AddsBalances(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v2.db")
	rawDB(t, path, schemaV2)

	st, err := Open(path)
	if err != nil {
		t.Fatalf("open v2 db: %v", err)
	}
	// Transactions are intact.
	items, err := st.List(ctx, ListFilter{})
	if err != nil || len(items) != 2 {
		t.Fatalf("transactions after migration: %v %v", items, err)
	}
	if items[0].Note != "chicken rice" || items[0].UpdatedAt == nil || items[1].Amount != "120.50" ||
		items[1].SpentAt != "2026-10-05T09:00:00+07:00" {
		t.Errorf("transaction data changed: %+v", items)
	}
	// The transactions AUTOINCREMENT counter is untouched (deleted id 3 not reused).
	e := items[1]
	e.ID = 0
	if err := st.Create(ctx, &e); err != nil || e.ID != 4 {
		t.Errorf("new transaction id = %d (%v), want 4", e.ID, err)
	}
	// Balances work.
	b := mustBalance(t, balance.Input{Name: "KBank debit", Type: "payment_account", Currency: "THB", Balance: dec("1500.25")})
	if err := st.CreateBalance(ctx, &b); err != nil || b.ID != 1 {
		t.Fatalf("create balance: id=%d err=%v", b.ID, err)
	}
	st.Close()
	assertCurrentSchema(t, path)

	// Re-open: no-op, data kept.
	st, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st.Close()
	got, err := st.GetBalance(ctx, 1)
	if err != nil || got.Balance == nil || *got.Balance != "1500.25" {
		t.Errorf("balance after reopen: %+v %v", got, err)
	}
	if n, _ := st.List(ctx, ListFilter{}); len(n) != 3 {
		t.Errorf("transactions after reopen: %d", len(n))
	}
}

func TestMigrationV3IdempotentIfTableExists(t *testing.T) {
	// balances already created (e.g. by hand) but version still 2.
	path := filepath.Join(t.TempDir(), "partial.db")
	rawDB(t, path, schemaV2+balancesSchemaV3)
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	st.Close()
	assertCurrentSchema(t, path)
}

func TestMigrationV3RefusesForeignBalancesTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "foreign.db")
	rawDB(t, path, schemaV2+`CREATE TABLE balances (id INTEGER PRIMARY KEY, amount REAL);`)
	if st, err := Open(path); err == nil {
		st.Close()
		t.Fatal("expected error for a balances table with an unexpected layout")
	} else if !strings.Contains(err.Error(), "lacks column") {
		t.Errorf("unexpected error: %v", err)
	}
	if v := userVersion(t, path); v != 2 {
		t.Errorf("user_version = %d, want 2 (untouched)", v)
	}
	if _, ok := schemaObjects(t, path)["idx_balances_type"]; ok {
		t.Error("index created despite rollback")
	}
}

func TestBalanceCRUDAndConstraints(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "b.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	card := mustBalance(t, balance.Input{Name: "UOB One", Type: "credit_card", Currency: "SGD", Debt: dec("1200"), Limit: dec("1000")})
	if err := st.CreateBalance(ctx, &card); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetBalance(ctx, card.ID)
	if *got.Debt != "1200.00" || *got.Limit != "1000.00" || *got.Available != "-200.00" || !*got.OverLimit || got.Balance != nil {
		t.Errorf("credit card read back: %+v", got)
	}

	// Case-insensitive unique name.
	dup := mustBalance(t, balance.Input{Name: "uob one", Type: "other_asset", Currency: "SGD", Balance: dec("1")})
	if err := st.CreateBalance(ctx, &dup); !errors.Is(err, ErrDuplicateName) {
		t.Errorf("duplicate name: got %v", err)
	}

	cash := mustBalance(t, balance.Input{Name: "Cash", Type: "payment_account", Currency: "THB", Balance: dec("-35")})
	if err := st.CreateBalance(ctx, &cash); err != nil {
		t.Fatal(err)
	}
	// Renaming onto an existing name (different case) is rejected too.
	if _, err := st.UpdateBalance(ctx, cash.ID, func(cur balance.Balance) (balance.Balance, error) {
		in := cur.Input()
		in.Name = "UOB ONE"
		return in.Validate()
	}); !errors.Is(err, ErrDuplicateName) {
		t.Errorf("rename to duplicate: got %v", err)
	}
	// Type change asset -> liability via update.
	upd, err := st.UpdateBalance(ctx, cash.ID, func(cur balance.Balance) (balance.Balance, error) {
		return balance.Input{Name: "Cash", Type: "other_liability", Currency: "THB", Debt: dec("10"), Limit: dec("0")}.Validate()
	})
	if err != nil || upd.Balance != nil || *upd.Debt != "10.00" || upd.UpdatedAt == nil {
		t.Errorf("type change: %+v %v", upd, err)
	}

	list, _ := st.ListBalances(ctx, "")
	if len(list) != 2 || list[0].Type != balance.CreditCard || list[1].Type != balance.OtherLiability {
		t.Errorf("list order: %+v", list)
	}
	if only, _ := st.ListBalances(ctx, balance.CreditCard); len(only) != 1 {
		t.Errorf("type filter: %d", len(only))
	}
	if err := st.DeleteBalance(ctx, card.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteBalance(ctx, card.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
	if _, err := st.GetBalance(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("get missing: %v", err)
	}

	// The CHECK constraints are a backstop below validation.
	db, _ := sql.Open("sqlite", "file:"+path)
	defer db.Close()
	for name, q := range map[string]string{
		"asset with debt":    `INSERT INTO balances (name,type,currency,amount_scale,balance_minor,debt_minor,created_at) VALUES ('x','payment_account','THB',2,1,1,'t')`,
		"liability no limit": `INSERT INTO balances (name,type,currency,amount_scale,debt_minor,created_at) VALUES ('y','credit_card','THB',2,1,'t')`,
		"negative debt":      `INSERT INTO balances (name,type,currency,amount_scale,debt_minor,limit_minor,created_at) VALUES ('z','credit_card','THB',2,-1,5,'t')`,
		"bad type":           `INSERT INTO balances (name,type,currency,amount_scale,balance_minor,created_at) VALUES ('w','wallet','THB',2,1,'t')`,
	} {
		if _, err := db.Exec(q); err == nil {
			t.Errorf("CHECK constraint did not reject %s", name)
		}
	}
}
