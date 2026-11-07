package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// openV4 creates a database at schema version 4, as an older build would
// have left it before migration v5.
func openV4(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range migrations[:4] {
		if err := step(tx); err != nil {
			t.Fatal(err)
		}
	}
	if err := setVersion(tx, 4); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// TestMigrationV5 upgrades a v4 database: the two redundant uid indexes are
// dropped, rows survive, uid uniqueness is still enforced by the UNIQUE
// constraints, uid lookups still use an index, and the schema matches a
// fresh database.
func TestMigrationV5(t *testing.T) {
	const (
		ownerUID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa1"
		walletID = "cccccccc-cccc-4ccc-8ccc-ccccccccccc3"
		txUID    = "dddddddd-dddd-4ddd-8ddd-ddddddddddd1"
	)
	dir := t.TempDir()
	oldPath, freshPath := filepath.Join(dir, "v4.db"), filepath.Join(dir, "fresh.db")
	openV4(t, oldPath)
	before := schemaObjects(t, oldPath)
	for _, k := range v5Dropped {
		if _, ok := before[k]; !ok {
			t.Fatalf("setup: v4 database lacks %s", k)
		}
	}
	db, err := sql.Open("sqlite", "file:"+oldPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO users (uid, username, display_name, password_hash, created_at)
		VALUES ('` + ownerUID + `', 'owner', 'Owner', 'x', '2026-01-01T00:00:00Z');
		INSERT INTO balances (uid, user_uid, name, type, currency, amount_scale, balance_minor, created_at, version)
		VALUES ('` + walletID + `', '` + ownerUID + `', 'Wallet', 'payment_account', 'THB', 2, 100000, '2026-02-01T00:00:00Z', 1);
		INSERT INTO transactions (uid, user_uid, type, amount_minor, amount_scale, currency, balance_uid, account, spent_at, spent_at_unix, created_at, version, balance_applied)
		VALUES ('` + txUID + `', '` + ownerUID + `', 'expense', 500, 2, 'THB', '` + walletID + `', 'Wallet', '2026-02-02T12:00:00+08:00', 1, '2026-02-02T12:00:00Z', 1, 0);
	`); err != nil {
		t.Fatalf("seed v4 data: %v", err)
	}
	db.Close()

	st, err := Open(oldPath)
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	st.Close()
	if v := pragmaInt(t, oldPath, "user_version"); v != SchemaVersion {
		t.Errorf("upgraded user_version = %d, want %d", v, SchemaVersion)
	}

	up := schemaObjects(t, oldPath)
	for _, k := range v5Dropped {
		if _, ok := up[k]; ok {
			t.Errorf("upgraded db still has %s", k)
		}
	}
	for _, k := range []string{"index sqlite_autoindex_transactions_1", "index sqlite_autoindex_balances_1"} {
		if _, ok := up[k]; !ok {
			t.Errorf("upgraded db missing %s (uid UNIQUE constraint index)", k)
		}
	}

	db, err = sql.Open("sqlite", "file:"+oldPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM transactions WHERE uid = ?`, txUID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("transaction after upgrade: n=%d err=%v", n, err)
	}
	if _, err := db.Exec(`INSERT INTO balances (uid, user_uid, name, type, currency, amount_scale, balance_minor, created_at, version)
		VALUES (?, ?, 'Wallet 2', 'payment_account', 'THB', 2, 0, '2026-02-01T00:00:00Z', 1)`, walletID, ownerUID); err == nil {
		t.Error("duplicate balance uid was accepted after dropping idx_balances_uid")
	}
	for table, uid := range map[string]string{"transactions": txUID, "balances": walletID} {
		rows, err := db.Query(`EXPLAIN QUERY PLAN SELECT id FROM `+table+` WHERE uid = ?`, uid)
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		rows.Close()
		if p := strings.Join(plan, "; "); !strings.Contains(p, "sqlite_autoindex_"+table+"_1") {
			t.Errorf("%s uid lookup does not use the UNIQUE index: %s", table, p)
		}
	}

	fresh, err := Open(freshPath)
	if err != nil {
		t.Fatal(err)
	}
	fresh.Close()
	fr := schemaObjects(t, freshPath)
	for _, k := range latestObjects {
		if up[k] == "" {
			t.Errorf("upgraded db missing %s", k)
		}
		if fr[k] != up[k] {
			t.Errorf("%s differs:\n fresh    %s\n upgraded %s", k, fr[k], up[k])
		}
	}
	for _, k := range v5Dropped {
		if _, ok := fr[k]; ok {
			t.Errorf("fresh db has %s", k)
		}
	}
}
