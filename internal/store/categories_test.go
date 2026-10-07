package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

const schemaV7 = schemaV6 + `
ALTER TABLE transactions ADD COLUMN type TEXT NOT NULL DEFAULT 'expense' CHECK (type IN ('expense', 'income', 'transfer'));
ALTER TABLE transactions ADD COLUMN to_balance_uid TEXT;
ALTER TABLE transactions ADD COLUMN to_account TEXT;
CREATE INDEX idx_transactions_type ON transactions (type, spent_at_unix DESC);
PRAGMA user_version = 7;
`

func TestMigrationV7ToV8AddsCategories(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v7.db")
	rawDB(t, path, schemaV7)
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open v7: %v", err)
	}
	items, err := st.List(ctx, ListFilter{})
	if err != nil || len(items) != 2 {
		t.Fatalf("list: %d %v", len(items), err)
	}
	for _, e := range items {
		if e.CategoryUID != nil {
			t.Errorf("row %d has category after migration: %v", e.ID, e.CategoryUID)
		}
	}
	cats, err := st.ListCategories(ctx, "")
	if err != nil || len(cats) != 0 {
		t.Errorf("categories: %v %v", cats, err)
	}
	st.Close()
	assertCurrentSchema(t, path)
}

func TestMigrationV8IdempotentIfPartlyApplied(t *testing.T) {
	path := filepath.Join(t.TempDir(), "partial8.db")
	rawDB(t, path, schemaV7+categoriesSchema+`
ALTER TABLE transactions ADD COLUMN category_uid TEXT;
`)
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	st.Close()
	assertCurrentSchema(t, path)
}

// schemaV8WithName is a database migrated by the pre-release v8 that still
// stored a category name snapshot in transactions.category.
const schemaV8WithName = schemaV7 + categoriesSchema + `
ALTER TABLE transactions ADD COLUMN category_uid TEXT CHECK (category_uid IS NULL OR length(category_uid) = 36);
ALTER TABLE transactions ADD COLUMN category TEXT;
CREATE INDEX idx_transactions_category_uid ON transactions (category_uid);
INSERT INTO categories (uid, name, type, created_at) VALUES ('cccccccc-cccc-4ccc-8ccc-cccccccccccc', 'Food', 'expense', '2026-10-08T00:00:00Z');
UPDATE transactions SET category_uid = 'cccccccc-cccc-4ccc-8ccc-cccccccccccc', category = 'Food' WHERE id = 1;
PRAGMA user_version = 8;
`

func TestMigrationV9DropsCategoryNameColumn(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v8name.db")
	rawDB(t, path, schemaV8WithName)
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	e, err := st.Get(ctx, 1)
	if err != nil || e.CategoryUID == nil || *e.CategoryUID != "cccccccc-cccc-4ccc-8ccc-cccccccccccc" {
		t.Fatalf("category_uid kept: %v %v", e.CategoryUID, err)
	}
	st.Close()
	assertCurrentSchema(t, path)
	if hasColumn(t, path, "transactions", "category") {
		t.Error("transactions.category still present")
	}
	// Reopen: idempotent.
	st, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	st.Close()
}

func TestFreshSchemaHasNoCategoryName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	if hasColumn(t, path, "transactions", "category") || !hasColumn(t, path, "transactions", "category_uid") {
		t.Error("fresh schema: want category_uid only")
	}
}

func hasColumn(t *testing.T, path, table, col string) bool {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info(?) WHERE name = ?`, table, col).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}
