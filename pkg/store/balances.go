package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/nutmos/expensetrackr/pkg/balance"
	"github.com/nutmos/expensetrackr/pkg/common_type"
	"github.com/nutmos/expensetrackr/pkg/money"
	"github.com/nutmos/expensetrackr/pkg/transaction"
)

// ErrBalanceInUse is returned when a balance's currency would change while
// transactions reference it (their amounts are in the old currency and are
// reversed against this balance when edited or deleted).
var ErrBalanceInUse = errors.New("balance is used by transactions")

// CreateBalance inserts a validated balance owned by userUID, assigns a new
// UUID v4 as UID, and fills in ID and CreatedAt. Any UID already on b is
// overwritten. Its beginning_balance transaction (the amount it starts with,
// dated now) is recorded in the same database transaction.
func (s *Store) CreateBalance(ctx context.Context, userUID string, b *balance.Balance) error {
	scale, ok := money.MinorUnits(b.Currency)
	if !ok {
		return fmt.Errorf("unknown currency %q", b.Currency)
	}
	uid, err := common_type.NewUID()
	if err != nil {
		return err
	}
	next := *b
	next.UID = uid
	next.Scale = scale
	next.CreatedAt = s.nowRFC3339()
	next.UpdatedAt, next.Version, next.Hidden = nil, 1, false
	err = s.inTx(ctx, "insert balance", func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx,
			`INSERT INTO balances (uid, user_uid, name, type, currency, description, amount_scale, balance_minor, debt_minor, limit_minor, created_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING id`,
			next.UID, userUID, next.Name, string(next.Type), next.Currency, next.Description, scale,
			nullInt(next.BalanceMinor), nullInt(next.DebtMinor), nullInt(next.LimitMinor), next.CreatedAt).Scan(&next.ID); err != nil {
			return fmt.Errorf("insert balance: %w", err)
		}
		return recordBeginningBalance(ctx, tx, userUID, next, next.CreatedAt, s.now())
	})
	if err != nil {
		return err
	}
	*b = next
	return nil
}

// ListBalances returns balances owned by userUID, grouped by type (payment
// accounts, credit cards, other assets, other liabilities), then by name.
// typ "" means all. Hidden balances are included (each has Hidden set);
// callers that show a picker leave them out.
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
	return scanBalances(rows)
}

// ListPayableBalances returns balances that can be used as a payment account
// on a transaction (payment_account and credit_card), ordered like ListBalances.
func (s *Store) ListPayableBalances(ctx context.Context, userUID string) ([]balance.Balance, error) {
	all, err := s.ListBalances(ctx, userUID, "")
	if err != nil {
		return nil, err
	}
	return balance.Filter(all, balance.Balance.IsPayable), nil
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
		cur, err := lockBalanceForWrite(ctx, tx, userUID, id, opts)
		if err != nil {
			return err
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
		next.Hidden = cur.Hidden // changed only by SetBalanceHidden
		// The type is immutable after creation (callers reject a change with 422
		// via balance.Input.ValidateUpdate); never write it here.
		if next.Type != cur.Type {
			return fmt.Errorf("update balance: type cannot change (%s -> %s)", cur.Type, next.Type)
		}
		currencyChange := next.Currency != cur.Currency
		if currencyChange {
			if err := checkCurrencyChangeable(ctx, tx, userUID, cur.UID); err != nil {
				return err
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
		next.Scale = scale
		if currencyChange {
			if err := resetBeginningBalance(ctx, tx, userUID, next, now); err != nil {
				return err
			}
		} else if err := recordBalanceAdjustment(ctx, tx, userUID, cur, next, now, s.now()); err != nil {
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

// SetBalanceHidden hides (hidden true) or shows again (false) balance id of
// userUID and returns it. Hiding changes nothing else: the balance's amounts
// and its transactions stay as they are, and transactions may still use it.
// Changing the flag sets UpdatedAt and increments Version; asking for the
// state the balance is already in changes nothing and returns it as it is.
//
// Returns ErrNotFound, or ErrVersionConflict when opts.Version > 0 and differs
// from the stored one.
func (s *Store) SetBalanceHidden(ctx context.Context, userUID string, id int64, opts WriteOptions, hidden bool) (balance.Balance, error) {
	var saved balance.Balance
	err := s.inTx(ctx, "hide balance", func(tx *sql.Tx) error {
		cur, err := lockBalanceForWrite(ctx, tx, userUID, id, opts)
		if err != nil {
			return err
		}
		if cur.Hidden == hidden {
			saved = cur
			return nil
		}
		now := s.nowRFC3339()
		if _, err := tx.ExecContext(ctx, `UPDATE balances SET hidden = $1, updated_at = $2, version = version + 1 WHERE id = $3`, hidden, now, id); err != nil {
			return fmt.Errorf("hide balance: %w", err)
		}
		cur.Hidden, cur.UpdatedAt, cur.Version = hidden, &now, cur.Version+1
		saved = cur
		return nil
	})
	if err != nil {
		return balance.Balance{}, err
	}
	return saved, nil
}

// lockBalanceForWrite loads balance id of userUID FOR UPDATE, so concurrent
// writes to it wait for this one. Returns ErrNotFound, or ErrVersionConflict
// when opts.Version > 0 and differs from the stored one.
func lockBalanceForWrite(ctx context.Context, tx *sql.Tx, userUID string, id int64, opts WriteOptions) (balance.Balance, error) {
	cur, err := scanBalance(tx.QueryRowContext(ctx, `SELECT `+balanceCols+` FROM balances WHERE id = $1 AND user_uid = $2 FOR UPDATE`, id, userUID))
	if errors.Is(err, sql.ErrNoRows) {
		return balance.Balance{}, ErrNotFound
	}
	if err != nil {
		return balance.Balance{}, err
	}
	if opts.Version > 0 && cur.Version != opts.Version {
		return balance.Balance{}, ErrVersionConflict
	}
	return cur, nil
}

// checkCurrencyChangeable returns ErrBalanceInUse if any transaction other
// than its beginning balance (which follows the balance) references balance uid.
func checkCurrencyChangeable(ctx context.Context, tx *sql.Tx, userUID string, uid common_type.UID) error {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM transactions WHERE user_uid = $1 AND (balance_uid = $2 OR to_balance_uid = $3) AND type <> $4`,
		userUID, uid, uid, string(transaction.BeginningBalance)).Scan(&n); err != nil {
		return fmt.Errorf("update balance: %w", err)
	}
	if n > 0 {
		return fmt.Errorf("%w (%d transaction(s)); its currency cannot change", ErrBalanceInUse, n)
	}
	return nil
}
