package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nutmos/expensetrackr/pkg/transaction"
)

// openV3 creates a database at schema version 3, as an older build would
// have left it before migration v4.
func openV3(t *testing.T, path string) {
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
	for _, step := range migrations[:3] {
		if err := step(tx); err != nil {
			t.Fatal(err)
		}
	}
	if err := setVersion(tx, 3); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// TestMigrationV4 upgrades a v3 database with transactions of every older
// type: rows survive unchanged, return_on_investment becomes insertable, and
// the schema matches a fresh database.
func TestMigrationV4(t *testing.T) {
	const (
		ownerUID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa1"
		walletID = "cccccccc-cccc-4ccc-8ccc-ccccccccccc3"
		goldUID  = "cccccccc-cccc-4ccc-8ccc-ccccccccccc4"
	)
	dir := t.TempDir()
	oldPath, freshPath := filepath.Join(dir, "v3.db"), filepath.Join(dir, "fresh.db")
	openV3(t, oldPath)
	if v := pragmaInt(t, oldPath, "user_version"); v != 3 {
		t.Fatalf("setup: user_version = %d", v)
	}
	db, err := sql.Open("sqlite", "file:"+oldPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO users (uid, username, display_name, password_hash, created_at)
		VALUES ('` + ownerUID + `', 'owner', 'Owner', 'x', '2026-01-01T00:00:00Z');
		INSERT INTO balances (uid, user_uid, name, type, currency, amount_scale, balance_minor, created_at, version)
		VALUES ('` + walletID + `', '` + ownerUID + `', 'Wallet', 'payment_account', 'THB', 2, 100000, '2026-02-01T00:00:00Z', 1),
		       ('` + goldUID + `', '` + ownerUID + `', 'Gold', 'other_asset', 'THB', 2, 50000, '2026-02-01T00:00:00Z', 1);
		INSERT INTO transactions (uid, user_uid, type, amount_minor, amount_scale, currency, balance_uid, account, to_balance_uid, to_account, adjustment_direction, spent_at, spent_at_unix, created_at, version, balance_applied)
		VALUES
			('dddddddd-dddd-4ddd-8ddd-ddddddddddd1', '` + ownerUID + `', 'expense', 500, 2, 'THB', '` + walletID + `', 'Wallet', NULL, NULL, NULL, '2026-02-02T12:00:00+08:00', 1, '2026-02-02T12:00:00Z', 1, 0),
			('dddddddd-dddd-4ddd-8ddd-ddddddddddd2', '` + ownerUID + `', 'income', 600, 2, 'THB', '` + walletID + `', 'Wallet', NULL, NULL, NULL, '2026-02-02T12:00:00+08:00', 2, '2026-02-02T12:00:00Z', 1, 0),
			('dddddddd-dddd-4ddd-8ddd-ddddddddddd3', '` + ownerUID + `', 'transfer', 700, 2, 'THB', '` + walletID + `', 'Wallet', '` + goldUID + `', 'Gold', NULL, '2026-02-02T12:00:00+08:00', 3, '2026-02-02T12:00:00Z', 1, 0),
			('dddddddd-dddd-4ddd-8ddd-ddddddddddd4', '` + ownerUID + `', 'asset_purchase', 800, 2, 'THB', '` + walletID + `', 'Wallet', '` + goldUID + `', 'Gold', NULL, '2026-02-02T12:00:00+08:00', 4, '2026-02-02T12:00:00Z', 1, 0),
			('dddddddd-dddd-4ddd-8ddd-ddddddddddd5', '` + ownerUID + `', 'balance_adjustment', 900, 2, 'THB', '` + walletID + `', 'Wallet', NULL, NULL, 'increase', '2026-02-02T12:00:00+08:00', 5, '2026-02-02T12:00:00Z', 1, 0);
	`); err != nil {
		t.Fatalf("seed v3 data: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO transactions (uid, user_uid, type, amount_minor, amount_scale, currency, balance_uid, account, spent_at, spent_at_unix, created_at)
		VALUES ('eeeeeeee-eeee-4eee-8eee-eeeeeeeeeee1', ?, 'return_on_investment', 1, 2, 'THB', ?, 'Wallet', '2026-02-02T12:00:00+08:00', 6, '2026-02-02T12:00:00Z')`, ownerUID, walletID); err == nil {
		t.Fatal("v3 must reject return_on_investment")
	}
	db.Close()

	st, err := Open(oldPath)
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	ctx := context.Background()
	txns, err := st.List(ctx, ownerUID, ListFilter{})
	if err != nil || len(txns) != 5 {
		t.Fatalf("rows after upgrade: %d %v", len(txns), err)
	}
	wantTypes := map[transaction.Type]bool{transaction.Expense: true, transaction.Income: true, transaction.Transfer: true, transaction.AssetPurchase: true, transaction.BalanceAdjustment: true}
	for _, e := range txns {
		if !wantTypes[e.Type] {
			t.Errorf("unexpected type %q", e.Type)
		}
		delete(wantTypes, e.Type)
	}
	if len(wantTypes) != 0 {
		t.Errorf("types lost in upgrade: %v", wantTypes)
	}
	gold, err := st.GetBalanceByUID(ctx, ownerUID, goldUID)
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := st.GetBalanceByUID(ctx, ownerUID, walletID)
	if err != nil {
		t.Fatal(err)
	}
	in := transaction.CreateInput{Type: string(transaction.ReturnOnInvestment), Amount: "12", Currency: "THB", BalanceUID: walletID, ToBalanceUID: goldUID, SpentAt: "2026-03-01T09:00:00+08:00"}
	e, err := in.Validate()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.AttachPaymentBalance(wallet); err != nil {
		t.Fatal(err)
	}
	if err := e.AttachDestinationBalance(gold); err != nil {
		t.Fatal(err)
	}
	if err := st.Create(ctx, ownerUID, &e); err != nil {
		t.Fatalf("insert return_on_investment after upgrade: %v", err)
	}
	if e.ID != 6 {
		t.Errorf("new id = %d, want 6 (AUTOINCREMENT kept)", e.ID)
	}
	st.Close()

	fresh, err := Open(freshPath)
	if err != nil {
		t.Fatal(err)
	}
	fresh.Close()
	if v := pragmaInt(t, oldPath, "user_version"); v != SchemaVersion {
		t.Errorf("upgraded user_version = %d, want %d", v, SchemaVersion)
	}
	up, fr := schemaObjects(t, oldPath), schemaObjects(t, freshPath)
	for _, k := range latestObjects {
		if up[k] == "" {
			t.Errorf("upgraded db missing %s", k)
		}
		if fr[k] != up[k] {
			t.Errorf("%s differs:\n fresh    %s\n upgraded %s", k, fr[k], up[k])
		}
	}
	if !strings.Contains(up["table transactions"], "'return_on_investment'") {
		t.Errorf("transactions DDL: %s", up["table transactions"])
	}
}
