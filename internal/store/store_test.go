package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"expense-service/internal/expense"
)

// oldSchemaV0 is the schema exactly as shipped before updated_at existed.
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

func TestMigrationFromOldSchema(t *testing.T) {
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

	if v := userVersion(t, path); v != SchemaVersion {
		t.Errorf("user_version = %d, want %d", v, SchemaVersion)
	}

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
	if v := userVersion(t, path); v != SchemaVersion {
		t.Errorf("user_version = %d, want %d", v, SchemaVersion)
	}
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
