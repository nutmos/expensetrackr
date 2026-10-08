package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"expense-service/internal/balance"
	"expense-service/internal/money"
	"expense-service/internal/transaction"
)

// schemaV2 is the v2 schema (table "transactions", no balances) with rows,
// including a deleted id so the AUTOINCREMENT counter is ahead of max(id).
const schemaV2 = `
CREATE TABLE transactions (
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
CREATE INDEX idx_transactions_spent_at_unix ON transactions (spent_at_unix DESC, id DESC);
PRAGMA user_version = 2;
`

func mustBalance(t *testing.T, in balance.Input) balance.Balance {
	t.Helper()
	b, err := in.Validate()
	if err != nil {
		t.Fatalf("validate %+v: %v", in, err)
	}
	return b
}

func dec(s string) *money.DecimalInput { d := money.DecimalInput(s); return &d }

func TestMigrationV2ToV3AddsBalances(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v2.db")
	rawDB(t, path, schemaV2)

	st, err := Open(path)
	if err != nil {
		t.Fatalf("open v2 db: %v", err)
	}
	// Empty transactions table migrated through to v5.
	items, err := st.List(ctx, ListFilter{})
	if err != nil || len(items) != 0 {
		t.Fatalf("transactions after migration: %v %v", items, err)
	}
	// Balances work on the new table.
	b := mustBalance(t, balance.Input{Name: "KBank debit", Type: "payment_account", Currency: "THB", Balance: dec("1500.25")})
	if err := st.CreateBalance(ctx, &b); err != nil || b.ID != 1 {
		t.Fatalf("create balance: id=%d err=%v", b.ID, err)
	}
	if !looksLikeUUID(b.UID) {
		t.Fatalf("create balance uid = %q, want UUID v4", b.UID)
	}
	e, err := transaction.CreateInput{Amount: "120.50", Currency: "THB", BalanceUID: b.UID, SpentAt: "2026-10-05T09:00:00+07:00"}.Validate()
	if err != nil {
		t.Fatal(err)
	}
	e.Account = b.Name
	if err := st.Create(ctx, &e); err != nil || e.ID != 1 {
		t.Fatalf("create transaction: id=%d err=%v", e.ID, err)
	}
	gotTxn, err := st.Get(ctx, 1)
	if err != nil || gotTxn.BalanceUID != b.UID || gotTxn.Account != "KBank debit" {
		t.Errorf("transaction: %+v %v", gotTxn, err)
	}
	st.Close()
	assertCurrentSchema(t, path)

	st, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st.Close()
	got, err := st.GetBalance(ctx, 1)
	if err != nil || got.Balance == nil || *got.Balance != "1500.25" || !looksLikeUUID(got.UID) {
		t.Errorf("balance after reopen: %+v %v", got, err)
	}
	if byUID, err := st.GetBalanceByUID(ctx, got.UID); err != nil || byUID.ID != 1 {
		t.Errorf("GetBalanceByUID: %+v %v", byUID, err)
	}
}

func TestMigrationV3IdempotentIfTableExists(t *testing.T) {
	// balances already created (e.g. by hand) but version still 2.
	path := filepath.Join(t.TempDir(), "partial.db")
	rawDB(t, path, schemaV2+balancesSchemaV3)
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	st.Close()
	assertCurrentSchema(t, path)
}

func TestMigrationV3RefusesForeignBalancesTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "foreign.db")
	rawDB(t, path, schemaV2+`CREATE TABLE balances (id INTEGER PRIMARY KEY, amount REAL);`)
	if st, err := Open(path); err == nil {
		st.Close()
		t.Fatal("expected error for a balances table with an unexpected layout")
	} else if !strings.Contains(err.Error(), "lacks column") {
		t.Errorf("unexpected error: %v", err)
	}
	if v := userVersion(t, path); v != 2 {
		t.Errorf("user_version = %d, want 2 (untouched)", v)
	}
	if _, ok := schemaObjects(t, path)["idx_balances_type"]; ok {
		t.Error("index created despite rollback")
	}
}

func TestBalanceCRUDAndConstraints(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "b.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	card := mustBalance(t, balance.Input{Name: "UOB One", Type: "credit_card", Currency: "SGD", Debt: dec("1200"), Limit: dec("1000")})
	if err := st.CreateBalance(ctx, &card); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetBalance(ctx, card.ID)
	if *got.Debt != "1200.00" || *got.Limit != "1000.00" || *got.Available != "-200.00" || !*got.OverLimit || got.Balance != nil {
		t.Errorf("credit card read back: %+v", got)
	}

	// Case-insensitive unique name.
	dup := mustBalance(t, balance.Input{Name: "uob one", Type: "other_asset", Currency: "SGD", Balance: dec("1")})
	if err := st.CreateBalance(ctx, &dup); !errors.Is(err, ErrDuplicateName) {
		t.Errorf("duplicate name: got %v", err)
	}

	cash := mustBalance(t, balance.Input{Name: "Cash", Type: "payment_account", Currency: "THB", Balance: dec("-35")})
	if err := st.CreateBalance(ctx, &cash); err != nil {
		t.Fatal(err)
	}
	// Renaming onto an existing name (different case) is rejected too.
	if _, err := st.UpdateBalance(ctx, cash.ID, func(cur balance.Balance) (balance.Balance, error) {
		in := cur.Input()
		in.Name = "UOB ONE"
		return in.Validate()
	}); !errors.Is(err, ErrDuplicateName) {
		t.Errorf("rename to duplicate: got %v", err)
	}
	// The type is immutable: even a callback that returns another type is refused
	// and nothing is written.
	if _, err := st.UpdateBalance(ctx, cash.ID, func(cur balance.Balance) (balance.Balance, error) {
		return balance.Input{Name: "Cash", Type: "other_liability", Currency: "THB", Debt: dec("10"), Limit: dec("0")}.Validate()
	}); err == nil {
		t.Errorf("type change via store: expected error")
	}
	upd, err := st.UpdateBalance(ctx, cash.ID, func(cur balance.Balance) (balance.Balance, error) {
		in := cur.Input()
		in.Balance = dec("12")
		return in.ValidateUpdate(cur.Type)
	})
	if err != nil || *upd.Balance != "12.00" || upd.Type != balance.PaymentAccount || upd.UpdatedAt == nil {
		t.Errorf("same-type update: %+v %v", upd, err)
	}

	list, _ := st.ListBalances(ctx, "")
	if len(list) != 2 || list[0].Type != balance.PaymentAccount || list[1].Type != balance.CreditCard {
		t.Errorf("list order: %+v", list)
	}
	if only, _ := st.ListBalances(ctx, balance.CreditCard); len(only) != 1 {
		t.Errorf("type filter: %d", len(only))
	}
	if err := st.DeleteBalance(ctx, card.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteBalance(ctx, card.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
	if _, err := st.GetBalance(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("get missing: %v", err)
	}

	// The CHECK constraints are a backstop below validation.
	db, _ := sql.Open("sqlite", "file:"+path)
	defer db.Close()
	for name, q := range map[string]string{
		"asset with debt":    `INSERT INTO balances (name,type,currency,amount_scale,balance_minor,debt_minor,created_at) VALUES ('x','payment_account','THB',2,1,1,'t')`,
		"liability no limit": `INSERT INTO balances (name,type,currency,amount_scale,debt_minor,created_at) VALUES ('y','credit_card','THB',2,1,'t')`,
		"negative debt":      `INSERT INTO balances (name,type,currency,amount_scale,debt_minor,limit_minor,created_at) VALUES ('z','credit_card','THB',2,-1,5,'t')`,
		"bad type":           `INSERT INTO balances (name,type,currency,amount_scale,balance_minor,created_at) VALUES ('w','wallet','THB',2,1,'t')`,
	} {
		if _, err := db.Exec(q); err == nil {
			t.Errorf("CHECK constraint did not reject %s", name)
		}
	}
}

func looksLikeUUID(s string) bool {
	// UUID v4 canonical form: 8-4-4-4-12 hex with version nibble 4.
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		case 14:
			if c != '4' {
				return false
			}
		default:
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
				return false
			}
		}
	}
	return true
}

// schemaV3 is the v3 schema (balances without uid) with two rows.
const schemaV3 = schemaV2 + `
CREATE TABLE balances (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    name          TEXT    NOT NULL COLLATE NOCASE UNIQUE CHECK (length(trim(name)) > 0),
    type          TEXT    NOT NULL CHECK (type IN ('payment_account', 'credit_card', 'other_asset', 'other_liability')),
    currency      TEXT    NOT NULL CHECK (length(currency) = 3),
    description   TEXT    NOT NULL DEFAULT '',
    amount_scale  INTEGER NOT NULL CHECK (amount_scale BETWEEN 0 AND 4),
    balance_minor INTEGER,
    debt_minor    INTEGER CHECK (debt_minor >= 0),
    limit_minor   INTEGER CHECK (limit_minor >= 0),
    created_at    TEXT    NOT NULL,
    updated_at    TEXT,
    CHECK (
        (type IN ('payment_account', 'other_asset')
            AND balance_minor IS NOT NULL AND debt_minor IS NULL AND limit_minor IS NULL)
        OR
        (type IN ('credit_card', 'other_liability')
            AND balance_minor IS NULL AND debt_minor IS NOT NULL AND limit_minor IS NOT NULL)
    )
);
CREATE INDEX idx_balances_type ON balances (type, name);
INSERT INTO balances (name, type, currency, description, amount_scale, balance_minor, debt_minor, limit_minor, created_at, updated_at) VALUES
  ('KBank debit', 'payment_account', 'THB', 'salary', 2, 1500050, NULL, NULL, '2026-10-07T00:00:00Z', NULL),
  ('UOB One', 'credit_card', 'SGD', '', 2, NULL, 120000, 100000, '2026-10-07T00:01:00Z', '2026-10-07T00:02:00Z');
PRAGMA user_version = 3;
`

func TestMigrationV3ToV4BackfillsUID(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v3.db")
	rawDB(t, path, schemaV3)

	st, err := Open(path)
	if err != nil {
		t.Fatalf("open v3 db: %v", err)
	}
	items, err := st.ListBalances(ctx, "")
	if err != nil || len(items) != 2 {
		t.Fatalf("balances: %d %v", len(items), err)
	}
	seen := map[string]bool{}
	for _, b := range items {
		if !looksLikeUUID(b.UID) {
			t.Errorf("backfilled uid = %q", b.UID)
		}
		if seen[b.UID] {
			t.Errorf("duplicate uid %q", b.UID)
		}
		seen[b.UID] = true
	}
	if items[0].Name != "KBank debit" || *items[0].Balance != "15000.50" ||
		items[1].Name != "UOB One" || *items[1].Debt != "1200.00" || !*items[1].OverLimit {
		t.Errorf("row data changed by migration: %+v", items)
	}
	st.Close()
	assertCurrentSchema(t, path)

	// Idempotent reopen.
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	again, _ := st.ListBalances(ctx, "")
	if again[0].UID != items[0].UID || again[1].UID != items[1].UID {
		t.Errorf("uids changed on reopen: %v vs %v", again, items)
	}
}

func TestMigrationV4IdempotentIfColumnExists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "partial.db")
	// Column added by hand / interrupted before version bump; one row still empty.
	rawDB(t, path, schemaV3+`
ALTER TABLE balances ADD COLUMN uid TEXT;
UPDATE balances SET uid = '11111111-1111-4111-8111-111111111111' WHERE id = 1;
`)
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	items, err := st.ListBalances(context.Background(), "")
	st.Close()
	if err != nil || len(items) != 2 {
		t.Fatalf("rows: %d %v", len(items), err)
	}
	if items[0].UID != "11111111-1111-4111-8111-111111111111" {
		t.Errorf("existing uid overwritten: %q", items[0].UID)
	}
	if !looksLikeUUID(items[1].UID) {
		t.Errorf("empty uid not backfilled: %q", items[1].UID)
	}
	assertCurrentSchema(t, path)
}

func TestBalanceUIDAssignedAndImmutable(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "uid.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	b := mustBalance(t, balance.Input{Name: "Cash", Type: "payment_account", Currency: "THB", Balance: dec("10")})
	b.UID = "client-supplied-should-be-ignored"
	if err := st.CreateBalance(ctx, &b); err != nil {
		t.Fatal(err)
	}
	if !looksLikeUUID(b.UID) || b.UID == "client-supplied-should-be-ignored" {
		t.Fatalf("uid = %q", b.UID)
	}
	orig := b.UID

	upd, err := st.UpdateBalance(ctx, b.ID, func(cur balance.Balance) (balance.Balance, error) {
		in := cur.Input()
		in.Balance = dec("20")
		in.UID = "00000000-0000-4000-8000-000000000000"
		return in.Validate()
	})
	if err != nil {
		t.Fatal(err)
	}
	if upd.UID != orig || *upd.Balance != "20.00" {
		t.Errorf("update changed uid or amount: %+v", upd)
	}
	got, err := st.GetBalanceByUID(ctx, orig)
	if err != nil || got.ID != b.ID {
		t.Errorf("GetBalanceByUID: %+v %v", got, err)
	}
	if _, err := st.GetBalanceByUID(ctx, "00000000-0000-4000-8000-000000000099"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing uid: %v", err)
	}
}

// schemaV4 is a v4 database: balances with uids, transactions still using free-text account only.
const schemaV4 = `
CREATE TABLE transactions (
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
CREATE INDEX idx_transactions_spent_at_unix ON transactions (spent_at_unix DESC, id DESC);
CREATE TABLE balances (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    uid           TEXT    NOT NULL UNIQUE CHECK (length(uid) = 36),
    name          TEXT    NOT NULL COLLATE NOCASE UNIQUE CHECK (length(trim(name)) > 0),
    type          TEXT    NOT NULL CHECK (type IN ('payment_account', 'credit_card', 'other_asset', 'other_liability')),
    currency      TEXT    NOT NULL CHECK (length(currency) = 3),
    description   TEXT    NOT NULL DEFAULT '',
    amount_scale  INTEGER NOT NULL CHECK (amount_scale BETWEEN 0 AND 4),
    balance_minor INTEGER,
    debt_minor    INTEGER CHECK (debt_minor >= 0),
    limit_minor   INTEGER CHECK (limit_minor >= 0),
    created_at    TEXT    NOT NULL,
    updated_at    TEXT,
    CHECK (
        (type IN ('payment_account', 'other_asset')
            AND balance_minor IS NOT NULL AND debt_minor IS NULL AND limit_minor IS NULL)
        OR
        (type IN ('credit_card', 'other_liability')
            AND balance_minor IS NULL AND debt_minor IS NOT NULL AND limit_minor IS NOT NULL)
    )
);
CREATE INDEX idx_balances_type ON balances (type, name);
CREATE UNIQUE INDEX idx_balances_uid ON balances (uid);
INSERT INTO balances (uid, name, type, currency, description, amount_scale, balance_minor, debt_minor, limit_minor, created_at) VALUES
  ('11111111-1111-4111-8111-111111111111', 'KBank debit', 'payment_account', 'THB', '', 2, 100000, NULL, NULL, '2026-10-07T00:00:00Z'),
  ('22222222-2222-4222-8222-222222222222', 'UOB One', 'credit_card', 'SGD', '', 2, NULL, 0, 500000, '2026-10-07T00:00:00Z'),
  ('33333333-3333-4333-8333-333333333333', 'Gold', 'other_asset', 'THB', '', 2, 8000000, NULL, NULL, '2026-10-07T00:00:00Z');
INSERT INTO transactions (amount_minor, amount_scale, currency, account, spent_at, spent_at_unix, note, created_at) VALUES
  (12050, 2, 'THB', 'KBank debit', '2026-10-05T09:00:00+07:00', 1791172800, 'pad thai', '2026-10-06T13:00:00Z'),
  (1890,  2, 'SGD', 'uob one', '2026-10-06T12:30:00+08:00', 1791267000, 'chicken rice', '2026-10-06T13:01:00Z');
PRAGMA user_version = 4;
`

func TestMigrationV4ToV5LinksAccountToBalanceUID(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v4.db")
	rawDB(t, path, schemaV4)

	st, err := Open(path)
	if err != nil {
		t.Fatalf("open v4 db: %v", err)
	}
	items, err := st.List(ctx, ListFilter{})
	if err != nil || len(items) != 2 {
		t.Fatalf("list: %d %v", len(items), err)
	}
	uids := map[string]string{}
	for _, e := range items {
		uids[e.Account] = e.BalanceUID
	}
	if uids["KBank debit"] != "11111111-1111-4111-8111-111111111111" {
		t.Errorf("KBank debit: %v", uids)
	}
	if uids["uob one"] != "22222222-2222-4222-8222-222222222222" {
		t.Errorf("uob one (case-insensitive match, original casing kept): %v", uids)
	}
	st.Close()
	assertCurrentSchema(t, path)
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

func TestMigrationV5FailsWhenAccountUnmatched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "orphan.db")
	rawDB(t, path, schemaV4+`
INSERT INTO transactions (amount_minor, amount_scale, currency, account, spent_at, spent_at_unix, note, created_at)
VALUES (100, 2, 'THB', 'Ghost Wallet', '2026-10-06T00:00:00Z', 1791244800, '', '2026-10-06T13:02:00Z');
`)
	if st, err := Open(path); err == nil {
		st.Close()
		t.Fatal("expected migration to fail for unmatched account")
	} else if !strings.Contains(err.Error(), "Ghost Wallet") && !strings.Contains(err.Error(), "1 transaction") {
		t.Errorf("unexpected error: %v", err)
	}
	if v := userVersion(t, path); v != 4 {
		t.Errorf("user_version = %d, want 4 (rolled back)", v)
	}
}

func TestMigrationV5IdempotentIfColumnExists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "partial.db")
	rawDB(t, path, schemaV4+`
ALTER TABLE transactions ADD COLUMN balance_uid TEXT;
UPDATE transactions SET balance_uid = '11111111-1111-4111-8111-111111111111' WHERE account = 'KBank debit';
`)
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	items, err := st.List(context.Background(), ListFilter{})
	st.Close()
	if err != nil || len(items) != 2 {
		t.Fatalf("rows: %d %v", len(items), err)
	}
	assertCurrentSchema(t, path)
}
