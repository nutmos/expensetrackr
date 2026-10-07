package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"expense-service/internal/balance"
	"expense-service/internal/money"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// ErrDuplicateName is returned when another balance already has the same
// name (compared case-insensitively).
var ErrDuplicateName = errors.New("a balance with this name already exists")

const balanceCols = `id, name, type, currency, description, amount_scale, balance_minor, debt_minor, limit_minor, created_at, updated_at`

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

// CreateBalance inserts a validated balance and fills in ID and CreatedAt.
func (s *Store) CreateBalance(ctx context.Context, b *balance.Balance) error {
	scale, ok := money.MinorUnits(b.Currency)
	if !ok {
		return fmt.Errorf("unknown currency %q", b.Currency)
	}
	b.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO balances (name, type, currency, description, amount_scale, balance_minor, debt_minor, limit_minor, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		b.Name, string(b.Type), b.Currency, b.Description, scale,
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

// GetBalance returns one balance or ErrNotFound.
func (s *Store) GetBalance(ctx context.Context, id int64) (balance.Balance, error) {
	b, err := scanBalance(s.db.QueryRowContext(ctx, `SELECT `+balanceCols+` FROM balances WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return balance.Balance{}, ErrNotFound
	}
	return b, err
}

// UpdateBalance loads balance id, passes it to fn and stores what fn returns,
// in one database transaction. ID and CreatedAt are preserved and UpdatedAt
// is set to now (UTC). Returns ErrNotFound, ErrDuplicateName, or fn's error.
func (s *Store) UpdateBalance(ctx context.Context, id int64, fn func(cur balance.Balance) (balance.Balance, error)) (balance.Balance, error) {
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
	next, err := fn(cur)
	if err != nil {
		return balance.Balance{}, err
	}
	scale, ok := money.MinorUnits(next.Currency)
	if !ok {
		return balance.Balance{}, fmt.Errorf("unknown currency %q", next.Currency)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	next.ID, next.CreatedAt, next.UpdatedAt = cur.ID, cur.CreatedAt, &now

	if _, err := tx.ExecContext(ctx,
		`UPDATE balances SET name = ?, type = ?, currency = ?, description = ?, amount_scale = ?,
		        balance_minor = ?, debt_minor = ?, limit_minor = ?, updated_at = ?
		 WHERE id = ?`,
		next.Name, string(next.Type), next.Currency, next.Description, scale,
		nullInt(next.BalanceMinor), nullInt(next.DebtMinor), nullInt(next.LimitMinor), now, id); err != nil {
		if mapped := mapBalanceErr(err); mapped == ErrDuplicateName {
			return balance.Balance{}, mapped
		}
		return balance.Balance{}, fmt.Errorf("update balance: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return balance.Balance{}, fmt.Errorf("update balance: %w", err)
	}
	return next, nil
}

// DeleteBalance removes one balance or returns ErrNotFound.
func (s *Store) DeleteBalance(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM balances WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete balance: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete balance: %w", err)
	}
	if n == 0 {
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
	if err := r.Scan(&b.ID, &b.Name, &typ, &b.Currency, &b.Description, &scale,
		&bal, &debt, &limit, &b.CreatedAt, &updated); err != nil {
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
