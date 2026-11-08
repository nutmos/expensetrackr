package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/nutmos/expensetrackr/pkg/balance"
	"github.com/nutmos/expensetrackr/pkg/common_type"
	"github.com/nutmos/expensetrackr/pkg/money"
)

// ErrBalanceInUse is returned when a balance's currency would change while
// transactions reference it (their amounts are in the old currency and are
// reversed against this balance when edited or deleted).
var ErrBalanceInUse = errors.New("balance is used by transactions")

const balanceCols = `id, uid, name, type, currency, description, amount_scale, balance_minor, debt_minor, limit_minor, created_at, updated_at, version`

func nullInt(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

// CreateBalance inserts a validated balance owned by userUID, assigns a new
// UUID v4 as UID, and fills in ID and CreatedAt. Any UID already on b is
// overwritten.
func (s *Store) CreateBalance(ctx context.Context, userUID string, b *balance.Balance) error {
	scale, ok := money.MinorUnits(b.Currency)
	if !ok {
		return fmt.Errorf("unknown currency %q", b.Currency)
	}
	uid, err := common_type.NewUID()
	if err != nil {
		return err
	}
	b.UID = uid
	b.CreatedAt = s.nowRFC3339()
	b.UpdatedAt, b.Version = nil, 1
	err = s.db.QueryRowContext(ctx,
		`INSERT INTO balances (uid, user_uid, name, type, currency, description, amount_scale, balance_minor, debt_minor, limit_minor, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING id`,
		b.UID, userUID, b.Name, string(b.Type), b.Currency, b.Description, scale,
		nullInt(b.BalanceMinor), nullInt(b.DebtMinor), nullInt(b.LimitMinor), b.CreatedAt).Scan(&b.ID)
	if err != nil {
		return fmt.Errorf("insert balance: %w", err)
	}
	return nil
}

// ListBalances returns balances owned by userUID, grouped by type (payment
// accounts, credit cards, other assets, other liabilities), then by name.
// typ "" means all.
func (s *Store) ListBalances(ctx context.Context, userUID string, typ balance.Type) ([]balance.Balance, error) {
	query := `SELECT ` + balanceCols + ` FROM balances WHERE user_uid = $1`
	args := []any{userUID}
	if typ != "" {
		args = append(args, string(typ))
		query += fmt.Sprintf(` AND type = $%d`, len(args))
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
func (s *Store) ListPayableBalances(ctx context.Context, userUID string) ([]balance.Balance, error) {
	all, err := s.ListBalances(ctx, userUID, "")
	if err != nil {
		return nil, err
	}
	out := make([]balance.Balance, 0, len(all))
	for _, b := range all {
		if b.Type == balance.TypePaymentAccount || b.Type == balance.TypeCreditCard {
			out = append(out, b)
		}
	}
	return out, nil
}

// GetBalance returns one balance by numeric id for userUID or ErrNotFound
// (including when the id belongs to another user).
func (s *Store) GetBalance(ctx context.Context, userUID string, id int64) (balance.Balance, error) {
	b, err := scanBalance(s.db.QueryRowContext(ctx, `SELECT `+balanceCols+` FROM balances WHERE id = $1 AND user_uid = $2`, id, userUID))
	if errors.Is(err, sql.ErrNoRows) {
		return balance.Balance{}, ErrNotFound
	}
	return b, err
}

// GetBalanceByUID returns one balance by its UUID for userUID or ErrNotFound
// (including when the uid belongs to another user).
func (s *Store) GetBalanceByUID(ctx context.Context, userUID string, uid common_type.UID) (balance.Balance, error) {
	b, err := scanBalance(s.db.QueryRowContext(ctx, `SELECT `+balanceCols+` FROM balances WHERE uid = $1 AND user_uid = $2`, uid, userUID))
	if errors.Is(err, sql.ErrNoRows) || isInvalidText(err) { // not a UUID: no such balance
		return balance.Balance{}, ErrNotFound
	}
	return b, err
}

// UpdateBalance is UpdateBalanceWith without a version check.
func (s *Store) UpdateBalance(ctx context.Context, userUID string, id int64, fn func(cur balance.Balance) (balance.Balance, error)) (balance.Balance, error) {
	return s.UpdateBalanceWith(ctx, userUID, id, WriteOptions{}, fn)
}

// UpdateBalanceWith loads balance id, passes it to fn and stores what fn
// returns, in one database transaction. This is the manual edit path (PUT): the
// amounts fn returns overwrite whatever transactions have done to
// the balance. ID, UID and CreatedAt are preserved, UpdatedAt is set to now
// (UTC) and Version is incremented. A change of the balance (asset) or debt
// (liability) amount is recorded as a balance_adjustment transaction in the
// same database transaction (see recordBalanceAdjustment); limit-only,
// name/description and currency changes record nothing.
//
// Returns ErrNotFound, ErrVersionConflict (opts.Version > 0 and stale),
// ErrBalanceInUse (currency change while transactions
// reference the balance), or fn's error.
func (s *Store) UpdateBalanceWith(ctx context.Context, userUID string, id int64, opts WriteOptions, fn func(cur balance.Balance) (balance.Balance, error)) (balance.Balance, error) {
	var saved balance.Balance
	err := s.inTx(ctx, "update balance", func(tx *sql.Tx) error {
		// FOR UPDATE: concurrent writes to this balance wait for this one.
		cur, err := scanBalance(tx.QueryRowContext(ctx, `SELECT `+balanceCols+` FROM balances WHERE id = $1 AND user_uid = $2 FOR UPDATE`, id, userUID))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if opts.Version > 0 && cur.Version != opts.Version {
			return ErrVersionConflict
		}
		next, err := fn(cur)
		if err != nil {
			return err
		}
		scale, ok := money.MinorUnits(next.Currency)
		if !ok {
			return fmt.Errorf("unknown currency %q", next.Currency)
		}
		now := s.nowRFC3339()
		next.ID, next.UID, next.CreatedAt, next.UpdatedAt = cur.ID, cur.UID, cur.CreatedAt, &now
		next.Version = cur.Version + 1
		// The type is immutable after creation (callers reject a change with 422
		// via balance.Input.ValidateUpdate); never write it here.
		if next.Type != cur.Type {
			return fmt.Errorf("update balance: type cannot change (%s -> %s)", cur.Type, next.Type)
		}
		if next.Currency != cur.Currency {
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM transactions WHERE user_uid = $1 AND (balance_uid = $2 OR to_balance_uid = $3)`, userUID, cur.UID, cur.UID).Scan(&n); err != nil {
				return fmt.Errorf("update balance: %w", err)
			}
			if n > 0 {
				return fmt.Errorf("%w (%d transaction(s)); its currency cannot change", ErrBalanceInUse, n)
			}
		}

		res, err := tx.ExecContext(ctx,
			`UPDATE balances SET name = $1, currency = $2, description = $3, amount_scale = $4,
		        balance_minor = $5, debt_minor = $6, limit_minor = $7, updated_at = $8, version = $9
		 WHERE id = $10 AND version = $11`,
			next.Name, next.Currency, next.Description, scale,
			nullInt(next.BalanceMinor), nullInt(next.DebtMinor), nullInt(next.LimitMinor), now, next.Version, id, cur.Version)
		if err != nil {
			return fmt.Errorf("update balance: %w", err)
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			return ErrVersionConflict
		}
		if err := recordBalanceAdjustment(ctx, tx, userUID, cur, next, now, s.now()); err != nil {
			return err
		}
		saved = next
		return nil
	})
	if err != nil {
		return balance.Balance{}, err
	}
	return saved, nil
}

// DeleteBalance is DeleteBalanceWith without a version check.
func (s *Store) DeleteBalance(ctx context.Context, userUID string, id int64) error {
	return s.DeleteBalanceWith(ctx, userUID, id, WriteOptions{})
}

// DeleteBalanceWith removes one balance owned by userUID. Returns ErrNotFound
// (including when the id belongs to another user), or ErrVersionConflict when
// opts.Version > 0 and differs from the stored one. Transactions that
// reference it keep its uid and name snapshot; editing or deleting them later
// skips the missing balance.
func (s *Store) DeleteBalanceWith(ctx context.Context, userUID string, id int64, opts WriteOptions) error {
	q, args := `DELETE FROM balances WHERE id = $1 AND user_uid = $2`, []any{id, userUID}
	if opts.Version > 0 {
		q, args = q+` AND version = $3`, append(args, opts.Version)
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
			if _, err := s.GetBalance(ctx, userUID, id); err == nil {
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
	var created time.Time
	var updated sql.NullTime
	if err := r.Scan(&b.ID, &b.UID, &b.Name, &typ, &b.Currency, &b.Description, &scale,
		&bal, &debt, &limit, &created, &updated, &b.Version); err != nil {
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
	b.CreatedAt, b.UpdatedAt = ts(created), nullTS(updated)
	return b, nil
}
