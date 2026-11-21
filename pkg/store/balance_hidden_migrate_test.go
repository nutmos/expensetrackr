package store

import (
	"context"
	"testing"
)

// TestMigrateV6ToV7: existing balances become visible (hidden = false) and
// the schema matches a fresh one.
func TestMigrateV6ToV7(t *testing.T) {
	url := testURL(t)
	buildV1(t, url)
	db := rawDB(t, url)
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations[1:6] {
		if err := m(ctx, tx); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO schema_migrations (version) VALUES (2), (3), (4), (5), (6)`); err != nil {
		t.Fatal(err)
	}
	const owner = "00000000-0000-4000-8000-000000000001"
	if _, err := db.Exec(`INSERT INTO users (uid, display_name, created_at) VALUES ($1, 'Owner', now())`, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO balances (uid, user_uid, name, type, currency, amount_scale, balance_minor, created_at)
		VALUES ('00000000-0000-4000-8000-0000000000b1', $1, 'Cash', 'payment_account', 'THB', 2, 100, now())`, owner); err != nil {
		t.Fatal(err)
	}

	st, err := Open(url)
	if err != nil {
		t.Fatalf("open v6: %v", err)
	}
	defer st.Close()
	if v := schemaVersion(t, url); v != SchemaVersion {
		t.Errorf("schema version after upgrade = %d, want %d", v, SchemaVersion)
	}
	list, err := st.ListBalances(ctx, owner, "")
	if err != nil || len(list) != 1 || list[0].Hidden {
		t.Errorf("balances after upgrade: %+v %v", list, err)
	}

	fresh := testURL(t)
	fst, err := Open(fresh)
	if err != nil {
		t.Fatal(err)
	}
	fst.Close()
	if got, want := portableSchema(t, url), portableSchema(t, fresh); got != want {
		t.Errorf("upgraded schema differs from a fresh one:\n got %v\nwant %v", got, want)
	}
}
