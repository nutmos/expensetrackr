// Package store persists transaction records in the SQLite table "transactions",
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
	"strings"
	"time"

	"github.com/nutmos/expensetrackr/pkg/money"
	"github.com/nutmos/expensetrackr/pkg/transaction"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// ErrNotFound is returned when no row in the transactions table has the given ID.
var ErrNotFound = errors.New("transaction not found")

// ErrVersionConflict is returned by an update or delete whose expected
// version (WriteOptions.Version) no longer matches the stored row: someone
// else changed it since the caller read it. Nothing is written.
var ErrVersionConflict = errors.New("version conflict: the record was changed since it was read")

// ErrReadOnly is returned when an update or delete targets a
// balance_adjustment transaction, or a write tries to give a transaction that
// type. Balance adjustments are created automatically by manual balance edits
// (UpdateBalanceWith) and are never changed afterwards.
var ErrReadOnly = errors.New("balance adjustments are created automatically and cannot be created, edited or deleted directly")

// WriteOptions controls optimistic locking for updates and deletes.
type WriteOptions struct {
	// Version, if > 0, must equal the stored version or the write fails with
	// ErrVersionConflict. 0 skips the check (internal callers and tests; the
	// API always sends the client's version for PUT/PATCH).
	Version int64
}

// Store wraps the SQLite database.
type Store struct {
	db   *sql.DB
	path string
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
	return &Store{db: db, path: path}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Create inserts a validated transaction as a new row in transactions, fills
// in UID, ID, CreatedAt and Version (1), and moves the referenced balances
// (see applyBalanceEffects), all in one database transaction. A referenced
// balance that is missing or in another currency is a *validate.ValidationError
// and nothing is written.
func (s *Store) Create(ctx context.Context, e *transaction.Transaction) error {
	if e.Type == transaction.BalanceAdjustment {
		return ErrReadOnly
	}
	if err := checkFX(*e); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("insert transaction: %w", err)
	}
	defer tx.Rollback() // no-op after Commit
	next, err := insertTx(ctx, tx, *e, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("insert transaction: %w", err)
	}
	*e = next
	return nil
}

// insertTx inserts e (already validated, not a balance adjustment) inside tx
// and applies its balance effects. A duplicate (balance_uid, import_ref) is a
// *DuplicateImportRefError.
func insertTx(ctx context.Context, tx *sql.Tx, e transaction.Transaction, now string) (transaction.Transaction, error) {
	scale, ok := money.MinorUnits(e.Currency)
	if !ok {
		return e, fmt.Errorf("unknown currency %q", e.Currency)
	}
	uid, err := newUID()
	if err != nil {
		return e, err
	}
	next := e
	next.UID, next.CreatedAt, next.UpdatedAt, next.Version, next.AdjustsBalances = uid, now, nil, 1, true
	res, err := tx.ExecContext(ctx,
		`INSERT INTO transactions (uid, type, amount_minor, amount_scale, currency, balance_uid, account, to_balance_uid, to_account, category_uid, spent_at, spent_at_unix, note, created_at, version, balance_applied,
		                           to_amount_minor, to_amount_scale, to_currency, exchange_rate, rate_source, rate_date, import_ref)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, 1, ?, ?, ?, ?, ?, ?, ?)`,
		append(append([]any{next.UID, string(next.Type), next.AmountMinor, scale, next.Currency, next.BalanceUID, next.Account, next.ToBalanceUID, next.ToAccount, next.CategoryUID, next.SpentAt, next.SpentTime.Unix(), next.Note, next.CreatedAt},
			fxArgs(next)...), next.ImportRef)...)
	if err != nil {
		return e, mapImportRefErr(ctx, tx, next, fmt.Errorf("insert transaction: %w", err))
	}
	id, err := res.LastInsertId()
	if err != nil {
		return e, fmt.Errorf("insert transaction: %w", err)
	}
	next.ID = id
	if err := applyBalanceEffects(ctx, tx, nil, &next, now); err != nil {
		return e, err
	}
	return next, nil
}

// ListFilter restricts List results. Zero values mean "no restriction".
type ListFilter struct {
	From        *time.Time       // inclusive
	To          *time.Time       // inclusive
	Type        transaction.Type // "" = all
	CategoryUID string           // "" = all
	Limit       int
}

const selectCols = `id, uid, type, amount_minor, amount_scale, currency, balance_uid, account, to_balance_uid, to_account, category_uid, spent_at, note, created_at, updated_at, version, balance_applied, adjustment_direction,
	to_amount_minor, to_amount_scale, to_currency, exchange_rate, rate_source, rate_date, import_ref`

// List returns rows from transactions newest first (by spend time, then ID).
func (s *Store) List(ctx context.Context, f ListFilter) ([]transaction.Transaction, error) {
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
	if f.CategoryUID != "" {
		query += ` AND category_uid = ?`
		args = append(args, f.CategoryUID)
	}
	if f.Type != "" {
		query += ` AND type = ?`
		args = append(args, string(f.Type))
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

	out := []transaction.Transaction{}
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
func (s *Store) Get(ctx context.Context, id int64) (transaction.Transaction, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+selectCols+` FROM transactions WHERE id = ?`, id)
	e, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return transaction.Transaction{}, ErrNotFound
	}
	return e, err
}

// GetByUID returns one row from transactions by its UUID or ErrNotFound.
func (s *Store) GetByUID(ctx context.Context, uid string) (transaction.Transaction, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+selectCols+` FROM transactions WHERE uid = ?`, strings.ToLower(strings.TrimSpace(uid)))
	e, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return transaction.Transaction{}, ErrNotFound
	}
	return e, err
}

// Update is UpdateWith without a version check.
func (s *Store) Update(ctx context.Context, id int64, fn func(cur transaction.Transaction) (transaction.Transaction, error)) (transaction.Transaction, error) {
	return s.UpdateWith(ctx, id, WriteOptions{}, fn)
}

// UpdateWith loads row id from transactions, passes it to fn and stores the
// transaction fn returns, all inside one database transaction (so a PATCH's
// read-modify-write is atomic). fn receives the current row and returns the
// new, already validated values; an error from fn aborts the update and is
// returned unchanged. ID, UID and CreatedAt are preserved, UpdatedAt is set
// to now (UTC) and Version is incremented.
//
// If opts.Version > 0 and differs from the stored version, ErrVersionConflict
// is returned. The old balance effect is reversed and the new one applied in
// the same database transaction (amount, type and account changes all work);
// a new balance that is missing or in another currency is a
// *validate.ValidationError. Returns ErrNotFound if the row does not exist.
func (s *Store) UpdateWith(ctx context.Context, id int64, opts WriteOptions, fn func(cur transaction.Transaction) (transaction.Transaction, error)) (transaction.Transaction, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return transaction.Transaction{}, fmt.Errorf("update transaction: %w", err)
	}
	defer tx.Rollback() // no-op after Commit

	cur, err := scan(tx.QueryRowContext(ctx, `SELECT `+selectCols+` FROM transactions WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return transaction.Transaction{}, ErrNotFound
	}
	if err != nil {
		return transaction.Transaction{}, err
	}
	if cur.Type == transaction.BalanceAdjustment {
		return transaction.Transaction{}, ErrReadOnly
	}
	if opts.Version > 0 && cur.Version != opts.Version {
		return transaction.Transaction{}, ErrVersionConflict
	}
	next, err := fn(cur)
	if err != nil {
		return transaction.Transaction{}, err
	}
	if next.Type == transaction.BalanceAdjustment {
		return transaction.Transaction{}, ErrReadOnly
	}
	next.AdjustmentDirection = nil
	if err := checkFX(next); err != nil {
		return transaction.Transaction{}, err
	}
	scale, ok := money.MinorUnits(next.Currency)
	if !ok {
		return transaction.Transaction{}, fmt.Errorf("unknown currency %q", next.Currency)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	next.ID, next.UID, next.CreatedAt, next.UpdatedAt = cur.ID, cur.UID, cur.CreatedAt, &now
	next.Version, next.AdjustsBalances = cur.Version+1, cur.AdjustsBalances

	res, err := tx.ExecContext(ctx,
		`UPDATE transactions SET type = ?, amount_minor = ?, amount_scale = ?, currency = ?, balance_uid = ?, account = ?, to_balance_uid = ?, to_account = ?, category_uid = ?,
		        spent_at = ?, spent_at_unix = ?, note = ?, updated_at = ?, version = ?,
		        to_amount_minor = ?, to_amount_scale = ?, to_currency = ?, exchange_rate = ?, rate_source = ?, rate_date = ?, import_ref = ?
		 WHERE id = ? AND version = ?`,
		append(append([]any{string(next.Type), next.AmountMinor, scale, next.Currency, next.BalanceUID, next.Account, next.ToBalanceUID, next.ToAccount, next.CategoryUID,
			next.SpentAt, next.SpentTime.Unix(), next.Note, now, next.Version}, fxArgs(next)...), next.ImportRef, id, cur.Version)...)
	if err != nil {
		return transaction.Transaction{}, mapImportRefErr(ctx, tx, next, fmt.Errorf("update transaction: %w", err))
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return transaction.Transaction{}, ErrVersionConflict
	}
	if err := applyBalanceEffects(ctx, tx, &cur, &next, now); err != nil {
		return transaction.Transaction{}, err
	}
	if err := tx.Commit(); err != nil {
		return transaction.Transaction{}, fmt.Errorf("update transaction: %w", err)
	}
	return next, nil
}

// Delete is DeleteWith without a version check.
func (s *Store) Delete(ctx context.Context, id int64) error {
	return s.DeleteWith(ctx, id, WriteOptions{})
}

// DeleteWith removes one row from transactions and reverses its balance
// effect, in one database transaction. Returns ErrNotFound, or
// ErrVersionConflict when opts.Version > 0 and differs from the stored one.
func (s *Store) DeleteWith(ctx context.Context, id int64, opts WriteOptions) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("delete transaction: %w", err)
	}
	defer tx.Rollback()

	cur, err := scan(tx.QueryRowContext(ctx, `SELECT `+selectCols+` FROM transactions WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if cur.Type == transaction.BalanceAdjustment {
		return ErrReadOnly
	}
	if opts.Version > 0 && cur.Version != opts.Version {
		return ErrVersionConflict
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM transactions WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete transaction: %w", err)
	}
	if err := applyBalanceEffects(ctx, tx, &cur, nil, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("delete transaction: %w", err)
	}
	return nil
}

// checkFX enforces the cross-currency invariants: the to_* fields are set
// together, only on a transfer, and only when to_currency differs.
func checkFX(e transaction.Transaction) error {
	if e.ToCurrency == nil && e.ToAmountMinor == nil {
		if e.ExchangeRate != nil || e.RateSource != nil || e.RateDate != nil {
			return errors.New("exchange_rate/rate_source/rate_date set without to_amount")
		}
		return nil
	}
	if e.Type != transaction.Transfer || e.ToCurrency == nil || e.ToAmountMinor == nil || *e.ToAmountMinor <= 0 ||
		e.ExchangeRate == nil || e.RateSource == nil || *e.ToCurrency == e.Currency {
		return fmt.Errorf("invalid cross-currency fields on a %s transaction", e.Type)
	}
	if _, ok := money.MinorUnits(*e.ToCurrency); !ok {
		return fmt.Errorf("unknown currency %q", *e.ToCurrency)
	}
	return nil
}

// fxArgs are the values of the to_amount_minor ... rate_date columns.
func fxArgs(e transaction.Transaction) []any {
	if e.ToCurrency == nil {
		return []any{nil, nil, nil, nil, nil, nil}
	}
	scale, _ := money.MinorUnits(*e.ToCurrency)
	return []any{*e.ToAmountMinor, scale, *e.ToCurrency, *e.ExchangeRate, *e.RateSource, e.RateDate}
}

type scanner interface{ Scan(dest ...any) error }

func scan(r scanner) (transaction.Transaction, error) {
	var e transaction.Transaction
	var scale int
	var updated, toUID, toName, catUID, dir sql.NullString
	var typ string
	var toMinor, toScale sql.NullInt64
	var toCur, rate, rateSrc, rateDate, importRef sql.NullString
	if err := r.Scan(&e.ID, &e.UID, &typ, &e.AmountMinor, &scale, &e.Currency, &e.BalanceUID, &e.Account, &toUID, &toName, &catUID, &e.SpentAt, &e.Note, &e.CreatedAt, &updated,
		&e.Version, &e.AdjustsBalances, &dir, &toMinor, &toScale, &toCur, &rate, &rateSrc, &rateDate, &importRef); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return e, err
		}
		return e, fmt.Errorf("scan transaction: %w", err)
	}
	e.Amount = money.FormatAmount(e.AmountMinor, scale)
	if updated.Valid {
		e.UpdatedAt = &updated.String
	}
	e.Type = transaction.Type(typ)
	if toUID.Valid {
		e.ToBalanceUID = &toUID.String
	}
	if toName.Valid {
		e.ToAccount = &toName.String
	}
	if catUID.Valid {
		e.CategoryUID = &catUID.String
	}
	if dir.Valid {
		d := transaction.Direction(dir.String)
		e.AdjustmentDirection = &d
	}
	if toMinor.Valid && toCur.Valid {
		m, a, c := toMinor.Int64, money.FormatAmount(toMinor.Int64, int(toScale.Int64)), toCur.String
		e.ToAmountMinor, e.ToAmount, e.ToCurrency = &m, &a, &c
		if rate.Valid {
			e.ExchangeRate = &rate.String
		}
		if rateSrc.Valid {
			e.RateSource = &rateSrc.String
		}
		if rateDate.Valid {
			e.RateDate = &rateDate.String
		}
	}
	if importRef.Valid {
		e.ImportRef = &importRef.String
	}
	if t, err := time.Parse(time.RFC3339, e.SpentAt); err == nil {
		e.SpentTime = t
	}
	return e, nil
}
