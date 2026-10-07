package store

import (
	"context"
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
		if e.CategoryUID != nil || e.Category != nil {
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
