package store

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
)

// Schema notes (table "transactions"):
//   - amount_minor: integer minor units (e.g. 12050 = 120.50 THB). No floats.
//   - amount_scale: number of decimal places used for amount_minor (ISO 4217
//     exponent at the time of writing), so each row is self-describing.
//   - spent_at:     RFC 3339 text exactly as entered (offset preserved),
//     e.g. 2026-10-06T21:06:00+08:00.
//   - spent_at_unix: the same instant as Unix seconds, used for ordering and
//     from/to filtering regardless of the offset each row was entered with.
//   - created_at / updated_at: RFC 3339 UTC; updated_at is NULL until edited.
//
// Schema history, tracked in SQLite's PRAGMA user_version:
//
//	v0  table "expenses" + index idx_expenses_spent_at_unix (original)
//	v1  expenses.updated_at TEXT added
//	v2  table renamed to "transactions", index to idx_transactions_spent_at_unix
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
