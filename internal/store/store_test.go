package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"expense-service/internal/expense"
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
INSERT INTO expenses (amount_minor, amount_scale, currency, account, spent_at, spent_at_unix, note, created_at)
VALUES (12050, 2, 'THB', 'KBank debit', '2026-10-06T21:06:00+08:00', 1791378360, 'lunch', '2026-10-06T13:06:30Z');
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
// Rows: id 1 (never edited), id 2 (edited), id 3 was inserted and deleted, so
// the AUTOINCREMENT counter (3) is ahead of max(id) (2).
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
INSERT INTO expenses (amount_minor, amount_scale, currency, account, spent_at, spent_at_unix, note, created_at, updated_at) VALUES
  (12050, 2, 'THB', 'KBank debit', '2026-10-05T09:00:00+07:00', 1791172800, 'pad thai', '2026-10-06T13:00:00Z', NULL),
  (1890,  2, 'SGD', 'Cash',        '2026-10-06T12:30:00+08:00', 1791267000, 'chicken rice', '2026-10-06T13:01:00Z', '2026-10-06T13:05:00Z'),
  (100,   2, 'USD', 'Card',        '2026-10-06T00:00:00Z',      1791244800, 'to delete', '2026-10-06T13:02:00Z', NULL);
DELETE FROM expenses WHERE id = 3;
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

func assertV2Schema(t *testing.T, path string) {
	t.Helper()
	got := schemaObjects(t, path)
	want := map[string]string{"transactions": "table", "idx_transactions_spent_at_unix": "index"}
	if len(got) != len(want) {
		t.Errorf("schema objects = %v, want exactly %v", got, want)
	}
	for name, typ := range want {
		if got[name] != typ {
			t.Errorf("missing %s %q; have %v", typ, name, got)
		}
	}
	if v := userVersion(t, path); v != 2 || SchemaVersion != 2 {
		t.Errorf("user_version = %d (SchemaVersion %d), want 2", v, SchemaVersion)
	}
}

func TestFreshDatabaseUsesTransactions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	e, err := expense.CreateInput{Amount: "5", Currency: "SGD", Account: "Cash", SpentAt: "2026-10-07T08:00:00+08:00"}.Validate()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Create(context.Background(), &e); err != nil || e.ID != 1 {
		t.Fatalf("create on fresh db: id=%d err=%v", e.ID, err)
	}
	st.Close()
	assertV2Schema(t, path)
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
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].ID != 2 || items[1].ID != 1 {
		t.Fatalf("rows after migration: %+v", items)
	}
	if items[0].Note != "chicken rice" || items[0].UpdatedAt == nil || *items[0].UpdatedAt != "2026-10-06T13:05:00Z" ||
		items[1].Amount != "120.50" || items[1].SpentAt != "2026-10-05T09:00:00+07:00" || items[1].UpdatedAt != nil {
		t.Errorf("row data changed by migration: %+v", items)
	}
	// AUTOINCREMENT state moves with the table: the deleted id 3 is not reused.
	e, _ := expense.CreateInput{Amount: "7", Currency: "THB", Account: "Cash", SpentAt: "2026-10-07T08:00:00+07:00"}.Validate()
	if err := st.Create(ctx, &e); err != nil || e.ID != 4 {
		t.Errorf("new row id = %d (err %v), want 4", e.ID, err)
	}
	if _, err := st.Update(ctx, 1, func(cur expense.Expense) (expense.Expense, error) {
		in := cur.Input()
		in.Note = "edited after rename"
		return in.Validate()
	}); err != nil {
		t.Errorf("update after rename: %v", err)
	}
	if err := st.Delete(ctx, 4); err != nil {
		t.Errorf("delete after rename: %v", err)
	}
	st.Close()
	assertV2Schema(t, path)

	// Re-opening is a no-op.
	st, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got, err := st.Get(ctx, 1); err != nil || got.Note != "edited after rename" {
		t.Errorf("after reopen: %+v %v", got, err)
	}
	st.Close()
	assertV2Schema(t, path)
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
			if err != nil || len(items) != 2 {
				t.Errorf("rows: %d %v", len(items), err)
			}
			assertV2Schema(t, path)
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
	e, err := st.Get(ctx, 1)
	if err != nil {
		t.Fatalf("existing row after migration: %v", err)
	}
	if e.Amount != "120.50" || e.Note != "lunch" || e.UpdatedAt != nil {
		t.Errorf("existing row changed by migration: %+v", e)
	}

	// The new column is usable.
	upd, err := st.Update(ctx, 1, func(cur expense.Expense) (expense.Expense, error) {
		in := cur.Input()
		in.Note = "edited"
		return in.Validate()
	})
	if err != nil || upd.UpdatedAt == nil || upd.CreatedAt != "2026-10-06T13:06:30Z" {
		t.Fatalf("update after migration: %+v, %v", upd, err)
	}
	st.Close()

	assertV2Schema(t, path)

	// Re-opening is a no-op and keeps data.
	st, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st.Close()
	e, err = st.Get(ctx, 1)
	if err != nil || e.Note != "edited" || e.UpdatedAt == nil {
		t.Errorf("after reopen: %+v, %v", e, err)
	}
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
	assertV2Schema(t, path)
}

func TestUpdateNotFound(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_, err = st.Update(context.Background(), 42, func(cur expense.Expense) (expense.Expense, error) { return cur, nil })
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}
