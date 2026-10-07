package store

import (
	"context"
	"path/filepath"
	"testing"

	"expense-service/internal/transaction"
)

const schemaV6 = schemaV5 + `
ALTER TABLE transactions ADD COLUMN uid TEXT;
UPDATE transactions SET uid = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa' || id;
CREATE UNIQUE INDEX idx_transactions_uid ON transactions (uid);
PRAGMA user_version = 6;
`

func TestMigrationV6ToV7DefaultsTypeExpense(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v6.db")
	rawDB(t, path, schemaV6)
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open v6: %v", err)
	}
	items, err := st.List(ctx, ListFilter{})
	if err != nil || len(items) != 2 {
		t.Fatalf("list: %d %v", len(items), err)
	}
	for _, e := range items {
		if e.Type != transaction.Expense || e.ToBalanceUID != nil || e.ToAccount != nil {
			t.Errorf("row %d: type=%q to=%v", e.ID, e.Type, e.ToBalanceUID)
		}
	}
	if inc, _ := st.List(ctx, ListFilter{Type: transaction.Income}); len(inc) != 0 {
		t.Errorf("income filter: %d", len(inc))
	}
	st.Close()
	assertCurrentSchema(t, path)
}

func TestMigrationV7IdempotentIfColumnExists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "partial7.db")
	rawDB(t, path, schemaV6+`
ALTER TABLE transactions ADD COLUMN type TEXT NOT NULL DEFAULT 'expense';
`)
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	st.Close()
	assertCurrentSchema(t, path)
}
