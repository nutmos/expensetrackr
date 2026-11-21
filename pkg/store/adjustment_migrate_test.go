package store

import (
	"context"
	"testing"
)

// TestMigrateV5ToV6: existing balance adjustments become applied (so editing
// or deleting them moves the balance), the old check against that is gone,
// the rest of it still holds, and the schema matches a fresh one.
func TestMigrateV5ToV6(t *testing.T) {
	url := testURL(t)
	buildV1(t, url)
	db := rawDB(t, url)
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations[1:5] {
		if err := m(ctx, tx); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO schema_migrations (version) VALUES (2), (3), (4), (5)`); err != nil {
		t.Fatal(err)
	}
	const owner = "00000000-0000-4000-8000-000000000001"
	if _, err := db.Exec(`INSERT INTO users (uid, display_name, created_at) VALUES ($1, 'Owner', now())`, owner); err != nil {
		t.Fatal(err)
	}
	insert := func(applied bool, category string) error {
		var cat any
		if category != "" {
			cat = category
		}
		_, err := db.Exec(`INSERT INTO transactions (uid, user_uid, type, amount_minor, amount_scale, currency, balance_uid, account, spent_at, spent_at_utc, created_at, balance_applied, adjustment_direction, category_uid)
			VALUES (gen_random_uuid(), $1, 'balance_adjustment', 100, 2, 'THB', '00000000-0000-4000-8000-0000000000b1', 'Cash', '2026-10-08T12:00:00+08:00', now(), now(), $2, 'increase', $3)`, owner, applied, cat)
		return err
	}
	if err := insert(false, ""); err != nil {
		t.Fatal(err)
	}
	if err := insert(true, ""); err == nil {
		t.Fatal("v5 accepted an applied adjustment; the test setup is wrong")
	}

	st, err := Open(url)
	if err != nil {
		t.Fatalf("open v5: %v", err)
	}
	defer st.Close()
	if v := schemaVersion(t, url); v != SchemaVersion {
		t.Errorf("schema version after upgrade = %d, want %d", v, SchemaVersion)
	}
	var unapplied int
	if err := db.QueryRow(`SELECT count(*) FROM transactions WHERE NOT balance_applied`).Scan(&unapplied); err != nil || unapplied != 0 {
		t.Errorf("unapplied rows = %d (%v), want 0", unapplied, err)
	}
	if err := insert(true, ""); err != nil {
		t.Errorf("applied adjustment after upgrade: %v", err)
	}
	if err := insert(true, "cccccccc-0000-4000-8000-000000000001"); err == nil {
		t.Error("adjustment with a category accepted")
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
