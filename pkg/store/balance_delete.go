package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/nutmos/expensetrackr/pkg/balance"
	"github.com/nutmos/expensetrackr/pkg/transaction"
)

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
		locked, txs, err := lockBalanceWithTransactions(ctx, tx, userUID, uid)
		if err != nil {
			return err
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
		if err := reverseBalanceEffects(ctx, tx, userUID, olds, s.nowRFC3339()); err != nil {
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
func balanceTransactionsTx(ctx context.Context, tx *sql.Tx, userUID, uid string) ([]transaction.Transaction, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+selectCols+` FROM transactions
		WHERE user_uid = $1 AND (balance_uid = $2 OR to_balance_uid = $2) ORDER BY id FOR UPDATE`, userUID, uid)
	if err != nil {
		return nil, fmt.Errorf("balance transactions: %w", err)
	}
	defer rows.Close()
	var out []transaction.Transaction
	for rows.Next() {
		e, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// lockBalanceWithTransactions locks balance uid, its transactions and every
// balance those transactions move, and returns the locked balances by uid and
// the transactions. Balances are locked in uid order like every transaction
// write (see applyBalanceEffects), so this waits for (or blocks) concurrent
// writes without deadlocking. The transactions are read again after each
// round of locking: one written in between is then included, and any balance
// it adds is locked too. The loop ends once a round locks nothing new.
func lockBalanceWithTransactions(ctx context.Context, tx *sql.Tx, userUID, uid string) (map[string]balance.Balance, []transaction.Transaction, error) {
	locked := map[string]balance.Balance{}
	for {
		txs, err := balanceTransactionsTx(ctx, tx, userUID, uid)
		if err != nil {
			return nil, nil, err
		}
		var missing []string
		for _, u := range touchedBalanceUIDs(uid, txs) {
			if _, ok := locked[u]; !ok {
				missing = append(missing, u)
			}
		}
		more, err := lockBalanceUIDs(ctx, tx, userUID, missing)
		if err != nil {
			return nil, nil, err
		}
		grew := false
		for u, b := range more {
			if _, ok := locked[u]; !ok {
				locked[u], grew = b, true
			}
		}
		if !grew {
			return locked, txs, nil
		}
	}
}

// touchedBalanceUIDs is uid plus every balance txs move (duplicates allowed).
func touchedBalanceUIDs(uid string, txs []transaction.Transaction) []string {
	uids := []string{uid}
	for i := range txs {
		for _, ef := range txs[i].Effects() {
			uids = append(uids, string(ef.BalanceUID))
		}
	}
	return uids
}
