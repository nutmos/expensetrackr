package store

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
)

// Schema notes, table "transactions" (one row per expense):
//   - amount_minor: integer minor units (e.g. 12050 = 120.50 THB). No floats.
//   - amount_scale: number of decimal places used for amount_minor (ISO 4217
//     exponent at the time of writing), so each row is self-describing.
//   - balance_uid:  UUID of the paying balance (payment_account or credit_card).
//     No FK: deleting a balance leaves the uid and the account name snapshot.
//   - account:      denormalized balance name at write time (display if the
//     balance is later renamed or deleted).
//   - spent_at:     RFC 3339 text exactly as entered (offset preserved),
//     e.g. 2026-10-06T21:06:00+08:00.
//   - spent_at_unix: the same instant as Unix seconds, used for ordering and
//     from/to filtering regardless of the offset each row was entered with.
//   - created_at / updated_at: RFC 3339 UTC; updated_at is NULL until edited.
//
// Table "balances" (see balancesSchemaCurrent): one row per payment account,
// credit card, other asset or other liability. CHECK constraints enforce the
// per-type amount columns; name is unique case-insensitively (ASCII folding,
// SQLite NOCASE); uid is a server-assigned UUID v4, immutable and unique.
//
// Schema history, tracked in SQLite's PRAGMA user_version:
//
//	v0  table "expenses" + index idx_expenses_spent_at_unix (original)
//	v1  expenses.updated_at TEXT added
//	v2  table renamed to "transactions", index to idx_transactions_spent_at_unix
//	v3  table "balances" + index idx_balances_type added
//	v4  balances.uid TEXT (UUID v4) added, backfilled, unique index idx_balances_uid
//	v5  transactions.balance_uid added (matched from free-text account where possible)
//	v6  transactions.uid TEXT (UUID v4) added, backfilled, unique index idx_transactions_uid
//	v7  transactions.type (expense|income|transfer, default expense), to_balance_uid, to_account
//
// A brand-new database is created directly at the latest version from
// currentSchema. An existing database is upgraded by running the pending
// migrations in order, each in its own SQL transaction together with the
// user_version bump, so an interrupted upgrade rolls back to the previous
// version. Every migration is also idempotent, as a second line of defence.

// currentSchema is the full latest schema, used only for new, empty databases.
// Keep it in sync with the result of applying every migration.
const currentSchema = `
CREATE TABLE transactions (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    uid           TEXT    NOT NULL UNIQUE CHECK (length(uid) = 36),
    amount_minor  INTEGER NOT NULL CHECK (amount_minor > 0),
    amount_scale  INTEGER NOT NULL CHECK (amount_scale BETWEEN 0 AND 4),
    currency      TEXT    NOT NULL CHECK (length(currency) = 3),
    balance_uid   TEXT    NOT NULL CHECK (length(balance_uid) = 36),
    account       TEXT    NOT NULL CHECK (length(account) > 0),
    type          TEXT    NOT NULL DEFAULT 'expense' CHECK (type IN ('expense', 'income', 'transfer')),
    to_balance_uid TEXT   CHECK (to_balance_uid IS NULL OR length(to_balance_uid) = 36),
    to_account    TEXT,
    spent_at      TEXT    NOT NULL,
    spent_at_unix INTEGER NOT NULL,
    note          TEXT    NOT NULL DEFAULT '',
    created_at    TEXT    NOT NULL,
    updated_at    TEXT
);
CREATE INDEX idx_transactions_spent_at_unix ON transactions (spent_at_unix DESC, id DESC);
CREATE INDEX idx_transactions_balance_uid ON transactions (balance_uid);
CREATE UNIQUE INDEX idx_transactions_uid ON transactions (uid);
CREATE INDEX idx_transactions_type ON transactions (type, spent_at_unix DESC);
` + balancesSchemaCurrent

// balancesSchemaV3 creates the balances table as shipped at version 3
// (no uid yet). Used only by migration v2 -> v3. Do not edit once shipped.
const balancesSchemaV3 = `
CREATE TABLE IF NOT EXISTS balances (
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
CREATE INDEX IF NOT EXISTS idx_balances_type ON balances (type, name);
`

// balancesSchemaCurrent is the full balances schema for brand-new databases
// (version 4+). Keep in sync with the result of applying every migration.
//
//   - uid: server-assigned UUID v4 (xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx),
//     immutable, unique; clients never set it
//   - type: payment_account | credit_card | other_asset | other_liability
//   - amount_scale: minor-unit digits of currency for the *_minor columns
//   - balance_minor: asset types only (required), may be negative (overdraft)
//   - debt_minor, limit_minor: liability types only (both required), >= 0;
//     debt may exceed limit (reported as over_limit by the API)
const balancesSchemaCurrent = `
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
`

// migration upgrades the schema by one version inside a SQL transaction.
type migration func(tx *sql.Tx) error

// migrations[i] upgrades from user_version i to i+1. Append only; never edit a
// migration that has shipped (they intentionally use the table names of their
// time, e.g. v0->v1 still refers to "expenses").
var migrations = []migration{
	// v0 -> v1: add updated_at (NULL until an expense is edited).
	func(tx *sql.Tx) error { return addColumnIfMissing(tx, "expenses", "updated_at", "TEXT") },
	// v1 -> v2: rename table expenses -> transactions and its index.
	renameExpensesToTransactions,
	// v2 -> v3: add the balances table.
	createBalances,
	// v3 -> v4: add balances.uid (UUID v4), backfill existing rows, unique index.
	addBalanceUID,
	// v4 -> v5: add transactions.balance_uid, match free-text account to balances.
	addTransactionBalanceUID,
	// v5 -> v6: add transactions.uid (UUID v4), backfill, unique index.
	addTransactionUID,
	// v6 -> v7: add transactions.type, to_balance_uid, to_account.
	addTransactionType,
}

// SchemaVersion is the user_version a fully migrated database has.
var SchemaVersion = len(migrations)

type queryer interface {
	QueryRow(query string, args ...any) *sql.Row
}

func objectExists(q queryer, typ, name string) (bool, error) {
	var n int
	err := q.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = ? AND name = ?`, typ, name).Scan(&n)
	return n > 0, err
}

func migrate(db *sql.DB) error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version > len(migrations) {
		return fmt.Errorf("database schema version %d is newer than this binary supports (%d)", version, len(migrations))
	}

	if version == 0 {
		hasOld, err := objectExists(db, "table", "expenses")
		if err != nil {
			return fmt.Errorf("inspect schema: %w", err)
		}
		hasNew, err := objectExists(db, "table", "transactions")
		if err != nil {
			return fmt.Errorf("inspect schema: %w", err)
		}
		switch {
		case !hasOld && !hasNew:
			return createFresh(db)
		case hasNew && !hasOld:
			return errors.New(`database has a "transactions" table but schema version 0; refusing to guess its layout`)
		}
		// hasOld: an original v0 database; fall through to the migrations.
	}

	for v := version; v < len(migrations); v++ {
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("migration %d: %w", v+1, err)
		}
		if err := migrations[v](tx); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", v+1, err)
		}
		if err := setVersion(tx, v+1); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", v+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("migration %d: commit: %w", v+1, err)
		}
		log.Printf("store: migrated schema to version %d", v+1)
	}
	return nil
}

// createFresh creates the latest schema in an empty database, atomically with
// setting user_version.
func createFresh(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	if _, err := tx.Exec(currentSchema); err != nil {
		tx.Rollback()
		return fmt.Errorf("create schema: %w", err)
	}
	if err := setVersion(tx, len(migrations)); err != nil {
		tx.Rollback()
		return fmt.Errorf("create schema: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("create schema: commit: %w", err)
	}
	log.Printf("store: created new database at schema version %d", len(migrations))
	return nil
}

func setVersion(tx *sql.Tx, v int) error {
	// PRAGMA does not accept bound parameters; v is a trusted integer.
	if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, v)); err != nil {
		return fmt.Errorf("set schema version: %w", err)
	}
	return nil
}

// renameExpensesToTransactions is migration v1 -> v2. It is idempotent: if the
// table was already renamed (e.g. by hand, or a run that got past the rename
// but not the version bump) it only fixes up the index. SQLite's RENAME TO
// keeps rows, AUTOINCREMENT state (sqlite_sequence) and attached indexes; the
// index keeps its old name, so it is dropped and recreated under the new name
// (SQLite has no ALTER INDEX ... RENAME).
func renameExpensesToTransactions(tx *sql.Tx) error {
	hasOld, err := objectExists(tx, "table", "expenses")
	if err != nil {
		return err
	}
	hasNew, err := objectExists(tx, "table", "transactions")
	if err != nil {
		return err
	}
	switch {
	case hasOld && hasNew:
		return errors.New(`both "expenses" and "transactions" tables exist; refusing to guess which holds the data (resolve manually, then restart)`)
	case hasOld:
		if _, err := tx.Exec(`ALTER TABLE expenses RENAME TO transactions`); err != nil {
			return fmt.Errorf("rename table: %w", err)
		}
	case hasNew:
		// Already renamed; nothing to do for the table.
	default:
		return errors.New(`neither "expenses" nor "transactions" table exists`)
	}
	if _, err := tx.Exec(`DROP INDEX IF EXISTS idx_expenses_spent_at_unix`); err != nil {
		return fmt.Errorf("drop old index: %w", err)
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_transactions_spent_at_unix ON transactions (spent_at_unix DESC, id DESC)`); err != nil {
		return fmt.Errorf("create index: %w", err)
	}
	return nil
}

// createBalances is migration v2 -> v3. CREATE ... IF NOT EXISTS makes it
// idempotent; it then checks that an already-existing balances table has the
// expected columns rather than silently accepting a different layout.
// Transactions are not touched (no foreign keys yet).
func createBalances(tx *sql.Tx) error {
	exists, err := objectExists(tx, "table", "balances")
	if err != nil {
		return err
	}
	if exists {
		if err := requireColumns(tx, "balances", "id", "name", "type", "currency", "description",
			"amount_scale", "balance_minor", "debt_minor", "limit_minor", "created_at", "updated_at"); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(balancesSchemaV3); err != nil {
		return fmt.Errorf("create balances: %w", err)
	}
	return nil
}

func requireColumns(tx *sql.Tx, table string, cols ...string) error {
	rows, err := tx.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return err
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		have[name] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, c := range cols {
		if !have[c] {
			return fmt.Errorf("existing table %q lacks column %q; refusing to continue", table, c)
		}
	}
	return nil
}

// addBalanceUID is migration v3 -> v4. It adds a nullable uid column if
// missing, fills every empty uid with a new UUID v4, then creates a unique
// index. SQLite cannot add a NOT NULL column without a default when rows
// already exist, so emptiness is enforced by the backfill + unique index and
// by the application (CreateBalance always sets uid). Brand-new databases use
// balancesSchemaCurrent, where uid is NOT NULL UNIQUE from the start.
func addBalanceUID(tx *sql.Tx) error {
	if err := addColumnIfMissing(tx, "balances", "uid", "TEXT"); err != nil {
		return fmt.Errorf("add uid column: %w", err)
	}
	rows, err := tx.Query(`SELECT id FROM balances WHERE uid IS NULL OR uid = ''`)
	if err != nil {
		return fmt.Errorf("list balances missing uid: %w", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		uid, err := newBalanceUID()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE balances SET uid = ? WHERE id = ?`, uid, id); err != nil {
			return fmt.Errorf("backfill uid for id %d: %w", id, err)
		}
	}
	if _, err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_balances_uid ON balances (uid)`); err != nil {
		return fmt.Errorf("create uid index: %w", err)
	}
	var missing int
	if err := tx.QueryRow(`SELECT count(*) FROM balances WHERE uid IS NULL OR uid = ''`).Scan(&missing); err != nil {
		return err
	}
	if missing > 0 {
		return fmt.Errorf("%d balances still missing uid after backfill", missing)
	}
	return nil
}

// addTransactionBalanceUID is migration v4 -> v5. It adds balance_uid, fills it
// by matching the free-text account column to a payable balance name
// (payment_account / credit_card, case-insensitive), then indexes the column.
// If any transaction cannot be matched the migration fails (nothing is
// committed) so the operator can fix the data. An empty transactions table
// succeeds. No foreign key is added: balances may still be deleted later; the
// uid and account name snapshot remain on the transaction.
func addTransactionBalanceUID(tx *sql.Tx) error {
	if err := addColumnIfMissing(tx, "transactions", "balance_uid", "TEXT"); err != nil {
		return fmt.Errorf("add balance_uid column: %w", err)
	}
	// Match free-text account to a payable balance by name (ASCII NOCASE).
	if _, err := tx.Exec(`
		UPDATE transactions
		   SET balance_uid = (
		     SELECT b.uid FROM balances b
		      WHERE b.name = transactions.account COLLATE NOCASE
		        AND b.type IN ('payment_account', 'credit_card')
		      LIMIT 1
		   )
		 WHERE balance_uid IS NULL OR balance_uid = ''
	`); err != nil {
		return fmt.Errorf("match account names to balances: %w", err)
	}
	var unmatched int
	if err := tx.QueryRow(`
		SELECT count(*) FROM transactions WHERE balance_uid IS NULL OR balance_uid = ''
	`).Scan(&unmatched); err != nil {
		return err
	}
	if unmatched > 0 {
		return fmt.Errorf("%d transaction(s) have an account name that does not match any payment_account or credit_card balance; create the missing balances (or delete the rows) and restart", unmatched)
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_transactions_balance_uid ON transactions (balance_uid)`); err != nil {
		return fmt.Errorf("create balance_uid index: %w", err)
	}
	return nil
}

// addTransactionUID is migration v5 -> v6, mirroring addBalanceUID (v4): add a
// nullable uid column if missing, fill every empty uid with a new UUID v4, then
// create a unique index. New databases get uid TEXT NOT NULL UNIQUE directly.
func addTransactionUID(tx *sql.Tx) error {
	if err := addColumnIfMissing(tx, "transactions", "uid", "TEXT"); err != nil {
		return fmt.Errorf("add transactions.uid column: %w", err)
	}
	rows, err := tx.Query(`SELECT id FROM transactions WHERE uid IS NULL OR uid = ''`)
	if err != nil {
		return fmt.Errorf("list transactions missing uid: %w", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		uid, err := newUID()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE transactions SET uid = ? WHERE id = ?`, uid, id); err != nil {
			return fmt.Errorf("backfill transaction uid for id %d: %w", id, err)
		}
	}
	if _, err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_transactions_uid ON transactions (uid)`); err != nil {
		return fmt.Errorf("create transactions uid index: %w", err)
	}
	return nil
}

// addTransactionType is migration v6 -> v7. Existing rows become 'expense'
// via the column default; to_balance_uid / to_account stay NULL. Idempotent.
func addTransactionType(tx *sql.Tx) error {
	cols := []struct{ name, decl string }{
		{"type", "TEXT NOT NULL DEFAULT 'expense' CHECK (type IN ('expense', 'income', 'transfer'))"},
		{"to_balance_uid", "TEXT CHECK (to_balance_uid IS NULL OR length(to_balance_uid) = 36)"},
		{"to_account", "TEXT"},
	}
	for _, c := range cols {
		if err := addColumnIfMissing(tx, "transactions", c.name, c.decl); err != nil {
			return fmt.Errorf("add transactions.%s: %w", c.name, err)
		}
	}
	if _, err := tx.Exec(`UPDATE transactions SET type = 'expense' WHERE type IS NULL OR type = ''`); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_transactions_type ON transactions (type, spent_at_unix DESC)`); err != nil {
		return fmt.Errorf("create type index: %w", err)
	}
	return nil
}

// addColumnIfMissing makes column additions idempotent, so a migration is safe
// even if a previous run added the column but crashed before bumping the version.
func addColumnIfMissing(tx *sql.Tx, table, column, decl string) error {
	rows, err := tx.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	_, err = tx.Exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, table, column, decl))
	return err
}
