package store

import (
	"context"
	"path/filepath"
	"regexp"
	"testing"
)

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// schemaV5 is schemaV4 migrated to v5 by hand: transactions with balance_uid, no uid.
const schemaV5 = schemaV4 + `
ALTER TABLE transactions ADD COLUMN balance_uid TEXT;
UPDATE transactions SET balance_uid = '11111111-1111-4111-8111-111111111111' WHERE account = 'KBank debit';
UPDATE transactions SET balance_uid = '22222222-2222-4222-8222-222222222222' WHERE account = 'uob one';
CREATE INDEX idx_transactions_balance_uid ON transactions (balance_uid);
PRAGMA user_version = 5;
`

func TestMigrationV5ToV6BackfillsTransactionUID(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v5.db")
	rawDB(t, path, schemaV5)
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open v5: %v", err)
	}
	items, err := st.List(ctx, ListFilter{})
	if err != nil || len(items) != 2 {
		t.Fatalf("list: %d %v", len(items), err)
	}
	seen := map[string]bool{}
	for _, e := range items {
		if !uuidV4.MatchString(e.UID) || seen[e.UID] {
			t.Errorf("bad/duplicate uid %q", e.UID)
		}
		seen[e.UID] = true
		got, err := st.GetByUID(ctx, e.UID)
		if err != nil || got.ID != e.ID {
			t.Errorf("GetByUID(%s) = %v, %v", e.UID, got.ID, err)
		}
	}
	st.Close()
	assertCurrentSchema(t, path)
	// Reopen: uids are stable.
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	again, _ := st.List(ctx, ListFilter{})
	for i := range again {
		if again[i].UID != items[i].UID {
			t.Errorf("uid changed on reopen")
		}
	}
}

func TestMigrationV6IdempotentKeepsExistingUID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "partial6.db")
	rawDB(t, path, schemaV5+`
ALTER TABLE transactions ADD COLUMN uid TEXT;
UPDATE transactions SET uid = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa' WHERE id = 1;
`)
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	e1, err := st.Get(context.Background(), 1)
	if err != nil || e1.UID != "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" {
		t.Errorf("existing uid overwritten: %q %v", e1.UID, err)
	}
	e2, _ := st.Get(context.Background(), 2)
	if !uuidV4.MatchString(e2.UID) {
		t.Errorf("empty uid not backfilled: %q", e2.UID)
	}
}
