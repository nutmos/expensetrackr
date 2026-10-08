package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nutmos/expensetrackr/pkg/balance"
	"github.com/nutmos/expensetrackr/pkg/transaction"
)

// oldSchemaV0 is the original schema (version 0: table "expenses", no updated_at).
const oldSchemaV0 = `
CREATE TABLE expenses (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    amount_minor  INTEGER NOT NULL CHECK (amount_minor > 0),
    amount_scale  INTEGER NOT NULL CHECK (amount_scale BETWEEN 0 AND 4),
    currency      TEXT    NOT NULL CHECK (length(currency) = 3),
    account       TEXT    NOT NULL CHECK (length(account) > 0),
    spent_at      TEXT    NOT NULL,
    spent_at_unix INTEGER NOT NULL,
    note          TEXT    NOT NULL DEFAULT '',
    created_at    TEXT    NOT NULL
);
CREATE INDEX idx_expenses_spent_at_unix ON expenses (spent_at_unix DESC, id DESC);
`

func userVersion(t *testing.T, path string) int {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// schemaV1 is the schema as shipped at version 1 (table still "expenses").
// Empty of rows so later migrations (v5 needs matching balances) can succeed;
// row-data migration is covered by the v4 -> v5 tests.
const schemaV1 = `
CREATE TABLE expenses (
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
CREATE INDEX idx_expenses_spent_at_unix ON expenses (spent_at_unix DESC, id DESC);
PRAGMA user_version = 1;
`

// rawDB creates a database file at path by running script directly.
func rawDB(t *testing.T, path, script string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(script); err != nil {
		t.Fatalf("setup script: %v", err)
	}
}

// schemaObjects lists user tables and indexes in the database at path.
func schemaObjects(t *testing.T, path string) map[string]string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT name, type FROM sqlite_master WHERE type IN ('table','index') AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, typ string
		if err := rows.Scan(&name, &typ); err != nil {
			t.Fatal(err)
		}
		out[name] = typ
	}
	return out
}

// assertCurrentSchema checks the database is fully migrated (latest version):
// every table and index, and user_version.
func assertCurrentSchema(t *testing.T, path string) {
	t.Helper()
	got := schemaObjects(t, path)
	want := map[string]string{
		"transactions": "table", "idx_transactions_spent_at_unix": "index", "idx_transactions_balance_uid": "index", "idx_transactions_uid": "index", "idx_transactions_type": "index", "idx_transactions_category_uid": "index", "categories": "table", "idx_categories_type_name": "index",
		"balances": "table", "idx_balances_type": "index", "idx_balances_uid": "index",
		"users": "table", "idx_users_username": "index", "idx_users_email": "index",
		"user_identities": "table", "idx_user_identities_user_uid": "index",
		"idx_transactions_to_balance_uid": "index",
	}
	if len(got) != len(want) {
		t.Errorf("schema objects = %v, want exactly %v", got, want)
	}
	for name, typ := range want {
		if got[name] != typ {
			t.Errorf("missing %s %q; have %v", typ, name, got)
		}
	}
	if v := userVersion(t, path); v != 13 || SchemaVersion != 13 {
		t.Errorf("user_version = %d (SchemaVersion %d), want 13", v, SchemaVersion)
	}
	// Every table has the same columns as a brand-new database (name, type,
	// NOT NULL, default; transactions too, since v13 rebuilds it from the
	// current definition), balances no longer forbids a negative debt, and
	// transactions accepts balance adjustments.
	fresh := filepath.Join(t.TempDir(), "fresh-compare.db")
	if path != fresh {
		st, err := Open(fresh)
		if err != nil {
			t.Fatal(err)
		}
		st.Close()
		for _, table := range []string{"transactions", "balances", "categories", "users", "user_identities"} {
			if got, want := tableColumns(t, path, table, true), tableColumns(t, fresh, table, true); got != want {
				t.Errorf("%s columns after migration:\n got %s\nwant %s", table, got, want)
			}
		}
	}
	if ddl := tableSQL(t, path, "balances"); strings.Contains(ddl, "debt_minor >= 0") || !strings.Contains(ddl, "version") {
		t.Errorf("balances DDL not at v11+: %s", ddl)
	}
	if ddl := tableSQL(t, path, "transactions"); !strings.Contains(ddl, "'balance_adjustment'") || !strings.Contains(ddl, "adjustment_direction") {
		t.Errorf("transactions DDL not at v13: %s", ddl)
	}
}

// tableColumns describes the columns of table as one comparable string.
func tableColumns(t *testing.T, path, table string, full bool) string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT name, type, "notnull", coalesce(dflt_value, '') FROM pragma_table_info(?) ORDER BY name`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var parts []string
	for rows.Next() {
		var name, typ, dflt string
		var notNull int
		if err := rows.Scan(&name, &typ, &notNull, &dflt); err != nil {
			t.Fatal(err)
		}
		if full {
			parts = append(parts, fmt.Sprintf("%s %s nn=%d d=%s", name, typ, notNull, dflt))
		} else {
			parts = append(parts, name+" "+typ)
		}
	}
	return strings.Join(parts, "; ")
}

func tableSQL(t *testing.T, path, table string) string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var ddl string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&ddl); err != nil {
		t.Fatal(err)
	}
	return strings.Join(strings.Fields(ddl), " ")
}

func TestFreshDatabaseUsesCurrentSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	b := mustBalance(t, balance.Input{Name: "Cash", Type: "payment_account", Currency: "SGD", Balance: dec("10")})
	if err := st.CreateBalance(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	e, err := transaction.CreateInput{Amount: "5", Currency: "SGD", BalanceUID: b.UID, SpentAt: "2026-10-07T08:00:00+08:00"}.Validate()
	if err != nil {
		t.Fatal(err)
	}
	e.Account = "Cash"
	if err := st.Create(context.Background(), &e); err != nil || e.ID != 1 {
		t.Fatalf("create on fresh db: id=%d err=%v", e.ID, err)
	}
	st.Close()
	assertCurrentSchema(t, path)
}

func TestMigrationV1ToV2RenamesTable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v1.db")
	rawDB(t, path, schemaV1)

	st, err := Open(path)
	if err != nil {
		t.Fatalf("open v1 db: %v", err)
	}
	items, err := st.List(ctx, ListFilter{})
	if err != nil || len(items) != 0 {
		t.Fatalf("rows after migration: %+v %v", items, err)
	}
	st.Close()
	assertCurrentSchema(t, path)
	st, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	st.Close()
	assertCurrentSchema(t, path)
}

func TestMigrationV2IdempotentAfterPartialRename(t *testing.T) {
	cases := map[string]string{
		// Table renamed by hand / earlier attempt, index still has the old name.
		"table renamed, old index name": schemaV1 + `ALTER TABLE expenses RENAME TO transactions;`,
		// Table renamed and index already recreated, but version not bumped.
		"table and index renamed": schemaV1 + `ALTER TABLE expenses RENAME TO transactions;
			DROP INDEX idx_expenses_spent_at_unix;
			CREATE INDEX idx_transactions_spent_at_unix ON transactions (spent_at_unix DESC, id DESC);`,
	}
	for name, script := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "partial.db")
			rawDB(t, path, script)
			st, err := Open(path)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			items, err := st.List(context.Background(), ListFilter{})
			st.Close()
			if err != nil || len(items) != 0 {
				t.Errorf("rows: %d %v", len(items), err)
			}
			assertCurrentSchema(t, path)
		})
	}
}

func TestMigrationV2RefusesAmbiguousSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "both.db")
	rawDB(t, path, schemaV1+`CREATE TABLE transactions (id INTEGER PRIMARY KEY);`)
	if st, err := Open(path); err == nil {
		st.Close()
		t.Fatal("expected an error when both expenses and transactions exist")
	}
	// Nothing was changed: still version 1 with both tables.
	if v := userVersion(t, path); v != 1 {
		t.Errorf("user_version = %d, want 1 (untouched)", v)
	}
	objs := schemaObjects(t, path)
	if objs["expenses"] != "table" || objs["transactions"] != "table" || objs["idx_expenses_spent_at_unix"] != "index" {
		t.Errorf("schema modified despite refusal: %v", objs)
	}
}

func TestMigrationFromV0Schema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")

	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(oldSchemaV0); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	if v := userVersion(t, path); v != 0 {
		t.Fatalf("precondition: user_version = %d", v)
	}

	st, err := Open(path)
	if err != nil {
		t.Fatalf("open old db: %v", err)
	}
	items, err := st.List(ctx, ListFilter{})
	if err != nil || len(items) != 0 {
		t.Fatalf("expected empty transactions after migrating empty v0: %v %v", items, err)
	}
	st.Close()
	assertCurrentSchema(t, path)

	st, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	st.Close()
	assertCurrentSchema(t, path)
}

func TestMigrationIdempotentIfColumnAlreadyAdded(t *testing.T) {
	// Simulates a crash after ALTER TABLE but before user_version was bumped.
	path := filepath.Join(t.TempDir(), "half.db")
	raw, _ := sql.Open("sqlite", "file:"+path)
	if _, err := raw.Exec(oldSchemaV0 + `ALTER TABLE expenses ADD COLUMN updated_at TEXT;`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open half-migrated db: %v", err)
	}
	st.Close()
	assertCurrentSchema(t, path)
}

func TestUpdateNotFound(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_, err = st.Update(context.Background(), 42, func(cur transaction.Transaction) (transaction.Transaction, error) { return cur, nil })
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}
