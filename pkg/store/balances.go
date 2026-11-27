package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nutmos/expensetrackr/pkg/balance"
	"github.com/nutmos/expensetrackr/pkg/common_type"
	"github.com/nutmos/expensetrackr/pkg/money"
	"github.com/nutmos/expensetrackr/pkg/transaction"
)

// ErrBalanceInUse is returned when a balance's currency would change while
// transactions reference it (their amounts are in the old currency and are
// reversed against this balance when edited or deleted).
var ErrBalanceInUse = errors.New("balance is used by transactions")

const balanceCols = `id, uid, name, type, currency, description, amount_scale, balance_minor, debt_minor, limit_minor, hidden, created_at, updated_at, version`

func nullInt(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

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
	name, desc, err := s.sealBalanceText(next)
	if err != nil {
		return err
	}
	err = s.inTx(ctx, "insert balance", func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx,
			`INSERT INTO balances (uid, user_uid, name, type, currency, description, amount_scale, balance_minor, debt_minor, limit_minor, created_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING id`,
			next.UID, userUID, name, string(next.Type), next.Currency, desc, scale,
			nullInt(next.BalanceMinor), nullInt(next.DebtMinor), nullInt(next.LimitMinor), next.CreatedAt).Scan(&next.ID); err != nil {
			return fmt.Errorf("insert balance: %w", err)
		}
		return s.recordBeginningBalance(ctx, tx, userUID, next, next.CreatedAt, s.now())
	})
	if err != nil {
		return err
	}
	*b = next
	return nil
}

// ListBalances returns balances owned by userUID, grouped by type (payment
// accounts, credit cards, other assets, other liabilities), then by name
// (case-insensitive; sorted here, since names may be stored encrypted).
// typ "" means all. Hidden balances are included (each has Hidden set);
// callers that show a picker leave them out.
func (s *Store) ListBalances(ctx context.Context, userUID string, typ balance.Type) ([]balance.Balance, error) {
	query := `SELECT ` + balanceCols + ` FROM balances WHERE user_uid = $1`
	args := []any{userUID}
	if typ != "" {
		args = append(args, string(typ))
		query += fmt.Sprintf(` AND type = $%d`, len(args))
	}
	query += ` ORDER BY id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list balances: %w", err)
	}
	defer rows.Close()
	out := []balance.Balance{}
	for rows.Next() {
		b, err := s.scanBalance(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sortBalances(out)
	return out, nil
}

// balanceTypeOrder is the position of each balance type in ListBalances.
var balanceTypeOrder = map[balance.Type]int{
	balance.TypePaymentAccount: 1, balance.TypeCreditCard: 2, balance.TypeOtherAsset: 3, balance.TypeOtherLiability: 4,
}

// sortBalances orders balances by type (balanceTypeOrder), then by name
// ignoring case, then by id (out is read in id order, and the sort is stable).
func sortBalances(out []balance.Balance) {
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if ta, tb := balanceTypeOrder[a.Type], balanceTypeOrder[b.Type]; ta != tb {
			return ta < tb
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
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
	b, err := s.scanBalance(s.db.QueryRowContext(ctx, `SELECT `+balanceCols+` FROM balances WHERE id = $1 AND user_uid = $2`, id, userUID))
	if errors.Is(err, sql.ErrNoRows) {
		return balance.Balance{}, ErrNotFound
	}
	return b, err
}

// GetBalanceByUID returns one balance by its UUID for userUID or ErrNotFound
// (including when the uid belongs to another user).
func (s *Store) GetBalanceByUID(ctx context.Context, userUID string, uid common_type.UID) (balance.Balance, error) {
	b, err := s.scanBalance(s.db.QueryRowContext(ctx, `SELECT `+balanceCols+` FROM balances WHERE uid = $1 AND user_uid = $2`, uid, userUID))
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
		cur, err := s.scanBalance(tx.QueryRowContext(ctx, `SELECT `+balanceCols+` FROM balances WHERE id = $1 AND user_uid = $2 FOR UPDATE`, id, userUID))
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
		next.Hidden = cur.Hidden // changed only by SetBalanceHidden
		// The type is immutable after creation (callers reject a change with 422
		// via balance.Input.ValidateUpdate); never write it here.
		if next.Type != cur.Type {
			return fmt.Errorf("update balance: type cannot change (%s -> %s)", cur.Type, next.Type)
		}
		currencyChange := next.Currency != cur.Currency
		if currencyChange {
			// The beginning balance does not count: it follows the balance.
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM transactions WHERE user_uid = $1 AND (balance_uid = $2 OR to_balance_uid = $3) AND type <> $4`,
				userUID, cur.UID, cur.UID, string(transaction.BeginningBalance)).Scan(&n); err != nil {
				return fmt.Errorf("update balance: %w", err)
			}
			if n > 0 {
				return fmt.Errorf("%w (%d transaction(s)); its currency cannot change", ErrBalanceInUse, n)
			}
		}

		name, desc, err := s.sealBalanceText(next)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE balances SET name = $1, currency = $2, description = $3, amount_scale = $4,
		        balance_minor = $5, debt_minor = $6, limit_minor = $7, updated_at = $8, version = $9
		 WHERE id = $10 AND version = $11`,
			name, next.Currency, desc, scale,
			nullInt(next.BalanceMinor), nullInt(next.DebtMinor), nullInt(next.LimitMinor), now, next.Version, id, cur.Version)
		if err != nil {
			return fmt.Errorf("update balance: %w", err)
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			return ErrVersionConflict
		}
		next.Scale = scale
		if currencyChange {
			if err := s.resetBeginningBalance(ctx, tx, userUID, next, now); err != nil {
				return err
			}
		} else if err := s.recordBalanceAdjustment(ctx, tx, userUID, cur, next, now, s.now()); err != nil {
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
		cur, err := s.scanBalance(tx.QueryRowContext(ctx, `SELECT `+balanceCols+` FROM balances WHERE id = $1 AND user_uid = $2 FOR UPDATE`, id, userUID))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if opts.Version > 0 && cur.Version != opts.Version {
			return ErrVersionConflict
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

// DeleteBalance is DeleteBalanceWith without a version check.
func (s *Store) DeleteBalance(ctx context.Context, userUID string, id int64) (int, error) {
	return s.DeleteBalanceWith(ctx, userUID, id, WriteOptions{})
}

// DeleteBalanceWith removes one balance owned by userUID together with every
// transaction that references it (as balance_uid or to_balance_uid, including
// the asset linked by a return on investment), so no transaction is left
// pointing at a missing balance. Each removed transaction's effect on the
// user's other balances is reversed, exactly as if it had been deleted on its
// own (e.g. removing a transfer from account A into the deleted balance puts
// the amount back on A). All of it happens in one database transaction.
//
// It returns the number of transactions removed. Errors: ErrNotFound
// (including when the id belongs to another user), or ErrVersionConflict when
// opts.Version > 0 and differs from the stored one; nothing is changed then.
func (s *Store) DeleteBalanceWith(ctx context.Context, userUID string, id int64, opts WriteOptions) (int, error) {
	var removed int
	err := s.inTx(ctx, "delete balance", func(tx *sql.Tx) error {
		var uid string
		err := tx.QueryRowContext(ctx, `SELECT uid FROM balances WHERE id = $1 AND user_uid = $2`, id, userUID).Scan(&uid)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("delete balance: %w", err)
		}
		// Lock this balance and every balance its transactions move, in uid
		// order like every transaction write (see applyBalanceEffects), so
		// this waits for (or blocks) concurrent writes without deadlocking.
		// The transactions are read again after locking: one written in
		// between is then included, and any balance it adds is locked too.
		locked := map[string]balance.Balance{}
		var txs []transaction.Transaction
		for {
			txs, err = s.balanceTransactionsTx(ctx, tx, userUID, uid)
			if err != nil {
				return err
			}
			uids := []string{uid}
			for i := range txs {
				for _, ef := range txs[i].Effects() {
					uids = append(uids, string(ef.BalanceUID))
				}
			}
			var missing []string
			for _, u := range uids {
				if _, ok := locked[u]; !ok {
					missing = append(missing, u)
				}
			}
			more, err := s.lockBalanceUIDs(ctx, tx, userUID, missing)
			if err != nil {
				return err
			}
			grew := false
			for u, b := range more {
				if _, ok := locked[u]; !ok {
					locked[u], grew = b, true
				}
			}
			if !grew {
				break
			}
		}
		cur, ok := locked[uid]
		if !ok {
			return ErrNotFound // deleted meanwhile
		}
		if opts.Version > 0 && cur.Version != opts.Version {
			return ErrVersionConflict
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM balances WHERE id = $1`, cur.ID); err != nil {
			return fmt.Errorf("delete balance: %w", err)
		}
		if len(txs) == 0 {
			return nil
		}
		ids := make([]int64, len(txs))
		olds := make([]*transaction.Transaction, len(txs))
		for i := range txs {
			ids[i], olds[i] = txs[i].ID, &txs[i]
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM transactions WHERE user_uid = $1 AND id = ANY($2::bigint[])`, userUID, ids); err != nil {
			return fmt.Errorf("delete balance transactions: %w", err)
		}
		// The deleted balance is gone, so only the other balances move.
		if err := s.reverseBalanceEffects(ctx, tx, userUID, olds, s.nowRFC3339()); err != nil {
			return err
		}
		removed = len(txs)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return removed, nil
}

// balanceTransactionsTx returns userUID's transactions that reference balance
// uid as balance_uid or to_balance_uid, locked FOR UPDATE.
func (s *Store) balanceTransactionsTx(ctx context.Context, tx *sql.Tx, userUID, uid string) ([]transaction.Transaction, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+selectCols+` FROM transactions
		WHERE user_uid = $1 AND (balance_uid = $2 OR to_balance_uid = $2) ORDER BY id FOR UPDATE`, userUID, uid)
	if err != nil {
		return nil, fmt.Errorf("balance transactions: %w", err)
	}
	defer rows.Close()
	var out []transaction.Transaction
	for rows.Next() {
		e, err := s.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) scanBalance(r scanner) (balance.Balance, error) {
	var b balance.Balance
	var typ string
	var scale int
	var bal, debt, limit sql.NullInt64
	var created time.Time
	var updated sql.NullTime
	if err := r.Scan(&b.ID, &b.UID, &b.Name, &typ, &b.Currency, &b.Description, &scale,
		&bal, &debt, &limit, &b.Hidden, &created, &updated, &b.Version); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return b, err
		}
		return b, fmt.Errorf("scan balance: %w", err)
	}
	if err := s.open(&b.Name, "balance name"); err != nil {
		return b, fmt.Errorf("scan balance %d: %w", b.ID, err)
	}
	if err := s.open(&b.Description, "balance description"); err != nil {
		return b, fmt.Errorf("scan balance %d: %w", b.ID, err)
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

// sealBalanceText returns the stored (encrypted) name and description of b.
func (s *Store) sealBalanceText(b balance.Balance) (name, desc string, err error) {
	if name, err = s.seal(b.Name); err != nil {
		return "", "", err
	}
	desc, err = s.seal(b.Description)
	return name, desc, err
}
