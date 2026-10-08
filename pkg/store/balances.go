package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/nutmos/expensetrackr/pkg/balance"
	"github.com/nutmos/expensetrackr/pkg/money"

	"github.com/google/uuid"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// ErrDuplicateName is returned when another balance already has the same
// name (compared case-insensitively).
var ErrDuplicateName = errors.New("a balance with this name already exists")

// ErrBalanceInUse is returned when a balance's currency would change while
// transactions reference it (their amounts are in the old currency and are
// reversed against this balance when edited or deleted).
var ErrBalanceInUse = errors.New("balance is used by transactions")

const balanceCols = `id, uid, name, type, currency, description, amount_scale, balance_minor, debt_minor, limit_minor, created_at, updated_at, version`

// newUID returns a random lowercase UUID v4 string (used for transactions).
func newUID() (string, error) {
	id, err := uuid.NewRandom()
	if err != nil {
		return "", fmt.Errorf("generate uid: %w", err)
	}
	return id.String(), nil
}

// newBalanceUID returns a random UUID v4 string
// (xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx, lowercase).
func newBalanceUID() (string, error) {
	id, err := uuid.NewRandom()
	if err != nil {
		return "", fmt.Errorf("generate balance uid: %w", err)
	}
	return id.String(), nil
}

// mapBalanceErr turns a UNIQUE violation on balances.name into ErrDuplicateName.
func mapBalanceErr(err error) error {
	var se *sqlite.Error
	if errors.As(err, &se) && se.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE {
		return ErrDuplicateName
	}
	return err
}

func nullInt(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

// CreateBalance inserts a validated balance, assigns a new UUID v4 as UID,
// and fills in ID and CreatedAt. Any UID already on b is overwritten.
func (s *Store) CreateBalance(ctx context.Context, b *balance.Balance) error {
	scale, ok := money.MinorUnits(b.Currency)
	if !ok {
		return fmt.Errorf("unknown currency %q", b.Currency)
	}
	uid, err := newBalanceUID()
	if err != nil {
		return err
	}
	b.UID = uid
	b.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	b.UpdatedAt, b.Version = nil, 1
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO balances (uid, name, type, currency, description, amount_scale, balance_minor, debt_minor, limit_minor, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		b.UID, b.Name, string(b.Type), b.Currency, b.Description, scale,
		nullInt(b.BalanceMinor), nullInt(b.DebtMinor), nullInt(b.LimitMinor), b.CreatedAt)
	if err != nil {
		if mapped := mapBalanceErr(err); mapped == ErrDuplicateName {
			return mapped
		}
		return fmt.Errorf("insert balance: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("insert balance: %w", err)
	}
	b.ID = id
	return nil
}

// ListBalances returns balances grouped by type (payment accounts, credit
// cards, other assets, other liabilities), then by name. typ "" means all.
func (s *Store) ListBalances(ctx context.Context, typ balance.Type) ([]balance.Balance, error) {
	query := `SELECT ` + balanceCols + ` FROM balances`
	var args []any
	if typ != "" {
		query += ` WHERE type = ?`
		args = append(args, string(typ))
	}
	query += ` ORDER BY CASE type WHEN 'payment_account' THEN 1 WHEN 'credit_card' THEN 2
	                              WHEN 'other_asset' THEN 3 ELSE 4 END, name, id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list balances: %w", err)
	}
	defer rows.Close()
	out := []balance.Balance{}
	for rows.Next() {
		b, err := scanBalance(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ListPayableBalances returns balances that can be used as a payment account
// on a transaction (payment_account and credit_card), ordered like ListBalances.
func (s *Store) ListPayableBalances(ctx context.Context) ([]balance.Balance, error) {
	all, err := s.ListBalances(ctx, "")
	if err != nil {
		return nil, err
	}
	out := make([]balance.Balance, 0, len(all))
	for _, b := range all {
		if b.Type == balance.PaymentAccount || b.Type == balance.CreditCard {
			out = append(out, b)
		}
	}
	return out, nil
}

// GetBalance returns one balance by numeric id or ErrNotFound.
func (s *Store) GetBalance(ctx context.Context, id int64) (balance.Balance, error) {
	b, err := scanBalance(s.db.QueryRowContext(ctx, `SELECT `+balanceCols+` FROM balances WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return balance.Balance{}, ErrNotFound
	}
	return b, err
}

// GetBalanceByUID returns one balance by its UUID or ErrNotFound.
func (s *Store) GetBalanceByUID(ctx context.Context, uid string) (balance.Balance, error) {
	b, err := scanBalance(s.db.QueryRowContext(ctx, `SELECT `+balanceCols+` FROM balances WHERE uid = ?`, uid))
	if errors.Is(err, sql.ErrNoRows) {
		return balance.Balance{}, ErrNotFound
	}
	return b, err
}

// UpdateBalance is UpdateBalanceWith without a version check.
func (s *Store) UpdateBalance(ctx context.Context, id int64, fn func(cur balance.Balance) (balance.Balance, error)) (balance.Balance, error) {
	return s.UpdateBalanceWith(ctx, id, WriteOptions{}, fn)
}

// UpdateBalanceWith loads balance id, passes it to fn and stores what fn
// returns, in one database transaction. This is the manual edit path (PUT /
// PATCH): the amounts fn returns overwrite whatever transactions have done to
// the balance. ID, UID and CreatedAt are preserved, UpdatedAt is set to now
// (UTC) and Version is incremented.
//
// Returns ErrNotFound, ErrVersionConflict (opts.Version > 0 and stale),
// ErrDuplicateName, ErrBalanceInUse (currency change while transactions
// reference the balance), or fn's error.
func (s *Store) UpdateBalanceWith(ctx context.Context, id int64, opts WriteOptions, fn func(cur balance.Balance) (balance.Balance, error)) (balance.Balance, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return balance.Balance{}, fmt.Errorf("update balance: %w", err)
	}
	defer tx.Rollback()

	cur, err := scanBalance(tx.QueryRowContext(ctx, `SELECT `+balanceCols+` FROM balances WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return balance.Balance{}, ErrNotFound
	}
	if err != nil {
		return balance.Balance{}, err
	}
	if opts.Version > 0 && cur.Version != opts.Version {
		return balance.Balance{}, ErrVersionConflict
	}
	next, err := fn(cur)
	if err != nil {
		return balance.Balance{}, err
	}
	scale, ok := money.MinorUnits(next.Currency)
	if !ok {
		return balance.Balance{}, fmt.Errorf("unknown currency %q", next.Currency)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	next.ID, next.UID, next.CreatedAt, next.UpdatedAt = cur.ID, cur.UID, cur.CreatedAt, &now
	next.Version = cur.Version + 1
	// The type is immutable after creation (callers reject a change with 422
	// via balance.Input.ValidateUpdate); never write it here.
	if next.Type != cur.Type {
		return balance.Balance{}, fmt.Errorf("update balance: type cannot change (%s -> %s)", cur.Type, next.Type)
	}
	if next.Currency != cur.Currency {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM transactions WHERE balance_uid = ? OR to_balance_uid = ?`, cur.UID, cur.UID).Scan(&n); err != nil {
			return balance.Balance{}, fmt.Errorf("update balance: %w", err)
		}
		if n > 0 {
			return balance.Balance{}, fmt.Errorf("%w (%d transaction(s)); its currency cannot change", ErrBalanceInUse, n)
		}
	}

	res, err := tx.ExecContext(ctx,
		`UPDATE balances SET name = ?, currency = ?, description = ?, amount_scale = ?,
		        balance_minor = ?, debt_minor = ?, limit_minor = ?, updated_at = ?, version = ?
		 WHERE id = ? AND version = ?`,
		next.Name, next.Currency, next.Description, scale,
		nullInt(next.BalanceMinor), nullInt(next.DebtMinor), nullInt(next.LimitMinor), now, next.Version, id, cur.Version)
	if err != nil {
		if mapped := mapBalanceErr(err); mapped == ErrDuplicateName {
			return balance.Balance{}, mapped
		}
		return balance.Balance{}, fmt.Errorf("update balance: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return balance.Balance{}, ErrVersionConflict
	}
	if err := tx.Commit(); err != nil {
		return balance.Balance{}, fmt.Errorf("update balance: %w", err)
	}
	return next, nil
}

// DeleteBalance is DeleteBalanceWith without a version check.
func (s *Store) DeleteBalance(ctx context.Context, id int64) error {
	return s.DeleteBalanceWith(ctx, id, WriteOptions{})
}

// DeleteBalanceWith removes one balance. Returns ErrNotFound, or
// ErrVersionConflict when opts.Version > 0 and differs from the stored one.
// Transactions that reference it keep its uid and name snapshot; editing or
// deleting them later skips the missing balance.
func (s *Store) DeleteBalanceWith(ctx context.Context, id int64, opts WriteOptions) error {
	q, args := `DELETE FROM balances WHERE id = ?`, []any{id}
	if opts.Version > 0 {
		q, args = q+` AND version = ?`, append(args, opts.Version)
	}
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("delete balance: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete balance: %w", err)
	}
	if n == 0 {
		if opts.Version > 0 {
			if _, err := s.GetBalance(ctx, id); err == nil {
				return ErrVersionConflict
			}
		}
		return ErrNotFound
	}
	return nil
}

func scanBalance(r scanner) (balance.Balance, error) {
	var b balance.Balance
	var typ string
	var scale int
	var bal, debt, limit sql.NullInt64
	var updated sql.NullString
	if err := r.Scan(&b.ID, &b.UID, &b.Name, &typ, &b.Currency, &b.Description, &scale,
		&bal, &debt, &limit, &b.CreatedAt, &updated, &b.Version); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return b, err
		}
		return b, fmt.Errorf("scan balance: %w", err)
	}
	b.Type = balance.Type(typ)
	ptr := func(n sql.NullInt64) *int64 {
		if !n.Valid {
			return nil
		}
		v := n.Int64
		return &v
	}
	b.SetAmounts(scale, ptr(bal), ptr(debt), ptr(limit))
	if updated.Valid {
		b.UpdatedAt = &updated.String
	}
	return b, nil
}
