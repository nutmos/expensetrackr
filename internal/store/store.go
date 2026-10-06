// Package store persists expenses in SQLite using the pure-Go modernc.org/sqlite
// driver (no CGO required).
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"expense-service/internal/expense"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// ErrNotFound is returned when an expense with the given ID does not exist.
var ErrNotFound = errors.New("expense not found")

// Schema notes:
//   - amount_minor: integer minor units (e.g. 12050 = 120.50 THB). No floats.
//   - amount_scale: number of decimal places used for amount_minor (ISO 4217
//     exponent at the time of writing), so each row is self-describing.
//   - spent_at:     RFC 3339 text exactly as entered (offset preserved),
//     e.g. 2026-10-06T21:06:00+08:00.
//   - spent_at_unix: the same instant as Unix seconds, used for ordering and
//     from/to filtering regardless of the offset each row was entered with.
//   - created_at / updated_at: RFC 3339 UTC; updated_at is NULL until edited.
//
// baseSchema is the original (version 0) schema. Later changes are applied by
// the numbered migrations below, tracked in SQLite's PRAGMA user_version, so an
// existing database file is upgraded in place without losing data.
const baseSchema = `
CREATE TABLE IF NOT EXISTS expenses (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    amount_minor  INTEGER NOT NULL CHECK (amount_minor > 0),
    amount_scale  INTEGER NOT NULL CHECK (amount_scale BETWEEN 0 AND 4),
    currency      TEXT    NOT NULL CHECK (length(currency) = 3),
    account       TEXT    NOT NULL CHECK (length(account) > 0),
    spent_at      TEXT    NOT NULL,
    spent_at_unix INTEGER NOT NULL,
    note          TEXT    NOT NULL DEFAULT '',
    created_at    TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_expenses_spent_at_unix ON expenses (spent_at_unix DESC, id DESC);
`

// migration upgrades the schema by one version inside a transaction.
type migration func(tx *sql.Tx) error

// migrations[i] upgrades from user_version i to i+1. Append only; never edit
// a migration that has shipped.
var migrations = []migration{
	// v0 -> v1: add updated_at (NULL until an expense is edited).
	func(tx *sql.Tx) error { return addColumnIfMissing(tx, "expenses", "updated_at", "TEXT") },
}

// SchemaVersion is the user_version a fully migrated database has.
var SchemaVersion = len(migrations)

// Store wraps the SQLite database.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the SQLite database at path and applies the schema.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db directory: %w", err)
		}
	}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(1)")
	dsn := "file:" + path + "?" + q.Encode()

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// A single connection keeps SQLite writes simple and safe for a small service.
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func migrate(db *sql.DB) error {
	if _, err := db.Exec(baseSchema); err != nil {
		return fmt.Errorf("apply base schema: %w", err)
	}
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version > len(migrations) {
		return fmt.Errorf("database schema version %d is newer than this binary supports (%d)", version, len(migrations))
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
		// PRAGMA does not accept bound parameters; v is a trusted integer.
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, v+1)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: set version: %w", v+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("migration %d: commit: %w", v+1, err)
		}
		log.Printf("store: migrated schema to version %d", v+1)
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

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Create inserts a validated expense and fills in ID and CreatedAt.
func (s *Store) Create(ctx context.Context, e *expense.Expense) error {
	scale, ok := expense.MinorUnits(e.Currency)
	if !ok {
		return fmt.Errorf("unknown currency %q", e.Currency)
	}
	e.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO expenses (amount_minor, amount_scale, currency, account, spent_at, spent_at_unix, note, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.AmountMinor, scale, e.Currency, e.Account, e.SpentAt, e.SpentTime.Unix(), e.Note, e.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert expense: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("insert expense: %w", err)
	}
	e.ID = id
	return nil
}

// ListFilter restricts List results. Zero values mean "no restriction".
type ListFilter struct {
	From  *time.Time // inclusive
	To    *time.Time // inclusive
	Limit int
}

const selectCols = `id, amount_minor, amount_scale, currency, account, spent_at, note, created_at, updated_at`

// List returns expenses newest first (by spend time, then ID).
func (s *Store) List(ctx context.Context, f ListFilter) ([]expense.Expense, error) {
	query := `SELECT ` + selectCols + ` FROM expenses WHERE 1=1`
	var args []any
	if f.From != nil {
		query += ` AND spent_at_unix >= ?`
		args = append(args, f.From.Unix())
	}
	if f.To != nil {
		query += ` AND spent_at_unix <= ?`
		args = append(args, f.To.Unix())
	}
	query += ` ORDER BY spent_at_unix DESC, id DESC`
	if f.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, f.Limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list expenses: %w", err)
	}
	defer rows.Close()

	out := []expense.Expense{}
	for rows.Next() {
		e, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Get returns one expense or ErrNotFound.
func (s *Store) Get(ctx context.Context, id int64) (expense.Expense, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+selectCols+` FROM expenses WHERE id = ?`, id)
	e, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return expense.Expense{}, ErrNotFound
	}
	return e, err
}

// Update loads expense id, passes it to fn and stores the expense fn returns,
// all inside one transaction (so a PATCH's read-modify-write is atomic). fn
// receives the current row and returns the new, already validated values; an
// error from fn aborts the update and is returned unchanged. ID and CreatedAt
// are preserved and UpdatedAt is set to now (UTC). Returns ErrNotFound if the
// expense does not exist.
func (s *Store) Update(ctx context.Context, id int64, fn func(cur expense.Expense) (expense.Expense, error)) (expense.Expense, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return expense.Expense{}, fmt.Errorf("update expense: %w", err)
	}
	defer tx.Rollback() // no-op after Commit

	cur, err := scan(tx.QueryRowContext(ctx, `SELECT `+selectCols+` FROM expenses WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return expense.Expense{}, ErrNotFound
	}
	if err != nil {
		return expense.Expense{}, err
	}
	next, err := fn(cur)
	if err != nil {
		return expense.Expense{}, err
	}
	scale, ok := expense.MinorUnits(next.Currency)
	if !ok {
		return expense.Expense{}, fmt.Errorf("unknown currency %q", next.Currency)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	next.ID, next.CreatedAt, next.UpdatedAt = cur.ID, cur.CreatedAt, &now

	if _, err := tx.ExecContext(ctx,
		`UPDATE expenses SET amount_minor = ?, amount_scale = ?, currency = ?, account = ?,
		        spent_at = ?, spent_at_unix = ?, note = ?, updated_at = ?
		 WHERE id = ?`,
		next.AmountMinor, scale, next.Currency, next.Account,
		next.SpentAt, next.SpentTime.Unix(), next.Note, now, id); err != nil {
		return expense.Expense{}, fmt.Errorf("update expense: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return expense.Expense{}, fmt.Errorf("update expense: %w", err)
	}
	return next, nil
}

// Delete removes one expense or returns ErrNotFound.
func (s *Store) Delete(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM expenses WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete expense: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete expense: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

type scanner interface{ Scan(dest ...any) error }

func scan(r scanner) (expense.Expense, error) {
	var e expense.Expense
	var scale int
	var updated sql.NullString
	if err := r.Scan(&e.ID, &e.AmountMinor, &scale, &e.Currency, &e.Account, &e.SpentAt, &e.Note, &e.CreatedAt, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return e, err
		}
		return e, fmt.Errorf("scan expense: %w", err)
	}
	e.Amount = expense.FormatAmount(e.AmountMinor, scale)
	if updated.Valid {
		e.UpdatedAt = &updated.String
	}
	if t, err := time.Parse(time.RFC3339, e.SpentAt); err == nil {
		e.SpentTime = t
	}
	return e, nil
}
