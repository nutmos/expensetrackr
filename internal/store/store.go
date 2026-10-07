// Package store persists expense records in the SQLite table "transactions",
// using the pure-Go modernc.org/sqlite driver (no CGO required). The schema and
// its migrations live in migrate.go.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"expense-service/internal/expense"
	"expense-service/internal/money"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// ErrNotFound is returned when no row in the transactions table has the given ID.
var ErrNotFound = errors.New("transaction not found")

// Store wraps the SQLite database.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the SQLite database at path and brings its
// schema up to date (see migrate).
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

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Create inserts a validated expense as a new row in transactions and fills in
// ID and CreatedAt.
func (s *Store) Create(ctx context.Context, e *expense.Expense) error {
	scale, ok := money.MinorUnits(e.Currency)
	if !ok {
		return fmt.Errorf("unknown currency %q", e.Currency)
	}
	e.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO transactions (amount_minor, amount_scale, currency, account, spent_at, spent_at_unix, note, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.AmountMinor, scale, e.Currency, e.Account, e.SpentAt, e.SpentTime.Unix(), e.Note, e.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert transaction: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("insert transaction: %w", err)
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

// List returns rows from transactions newest first (by spend time, then ID).
func (s *Store) List(ctx context.Context, f ListFilter) ([]expense.Expense, error) {
	query := `SELECT ` + selectCols + ` FROM transactions WHERE 1=1`
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
		return nil, fmt.Errorf("list transactions: %w", err)
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

// Get returns one row from transactions or ErrNotFound.
func (s *Store) Get(ctx context.Context, id int64) (expense.Expense, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+selectCols+` FROM transactions WHERE id = ?`, id)
	e, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return expense.Expense{}, ErrNotFound
	}
	return e, err
}

// Update loads row id from transactions, passes it to fn and stores the expense
// fn returns, all inside one database transaction (so a PATCH's read-modify-write is atomic). fn
// receives the current row and returns the new, already validated values; an
// error from fn aborts the update and is returned unchanged. ID and CreatedAt
// are preserved and UpdatedAt is set to now (UTC). Returns ErrNotFound if the
// row does not exist.
func (s *Store) Update(ctx context.Context, id int64, fn func(cur expense.Expense) (expense.Expense, error)) (expense.Expense, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return expense.Expense{}, fmt.Errorf("update transaction: %w", err)
	}
	defer tx.Rollback() // no-op after Commit

	cur, err := scan(tx.QueryRowContext(ctx, `SELECT `+selectCols+` FROM transactions WHERE id = ?`, id))
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
	scale, ok := money.MinorUnits(next.Currency)
	if !ok {
		return expense.Expense{}, fmt.Errorf("unknown currency %q", next.Currency)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	next.ID, next.CreatedAt, next.UpdatedAt = cur.ID, cur.CreatedAt, &now

	if _, err := tx.ExecContext(ctx,
		`UPDATE transactions SET amount_minor = ?, amount_scale = ?, currency = ?, account = ?,
		        spent_at = ?, spent_at_unix = ?, note = ?, updated_at = ?
		 WHERE id = ?`,
		next.AmountMinor, scale, next.Currency, next.Account,
		next.SpentAt, next.SpentTime.Unix(), next.Note, now, id); err != nil {
		return expense.Expense{}, fmt.Errorf("update transaction: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return expense.Expense{}, fmt.Errorf("update transaction: %w", err)
	}
	return next, nil
}

// Delete removes one row from transactions or returns ErrNotFound.
func (s *Store) Delete(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM transactions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete transaction: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete transaction: %w", err)
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
		return e, fmt.Errorf("scan transaction: %w", err)
	}
	e.Amount = money.FormatAmount(e.AmountMinor, scale)
	if updated.Valid {
		e.UpdatedAt = &updated.String
	}
	if t, err := time.Parse(time.RFC3339, e.SpentAt); err == nil {
		e.SpentTime = t
	}
	return e, nil
}
