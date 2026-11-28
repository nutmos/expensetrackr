package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Migration 5 -> 6: balance adjustments become editable and deletable.
//
// A balance_adjustment row records a manual balance edit that already set the
// balance, so its change is reflected in the balance like any other
// transaction's: balance_applied is now true for it (it was always false), and
// editing or deleting it moves the balance (see transaction.Effects). The
// baseline's unnamed check that forbade balance_applied on adjustments is
// replaced by transactions_adjustment_check, which keeps the rest of it (no
// to_balance_uid and no category).
func allowAdjustmentEdits(ctx context.Context, tx *sql.Tx) error {
	var name string
	err := tx.QueryRowContext(ctx, `SELECT conname FROM pg_constraint
		WHERE conrelid = 'transactions'::regclass AND contype = 'c'
		  AND pg_get_constraintdef(oid) LIKE '%balance_adjustment%' AND pg_get_constraintdef(oid) LIKE '%balance_applied%'`).Scan(&name)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("find adjustment check: %w", err)
	} else if err == nil { // on sql.ErrNoRows the check is already gone
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE transactions DROP CONSTRAINT %q`, name)); err != nil {
			return fmt.Errorf("drop adjustment check: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE transactions SET balance_applied = true WHERE type = 'balance_adjustment' AND NOT balance_applied`); err != nil {
		return fmt.Errorf("mark adjustments applied: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE transactions ADD CONSTRAINT transactions_adjustment_check
		CHECK (type <> 'balance_adjustment' OR (to_balance_uid IS NULL AND category_uid IS NULL))`); err != nil {
		return fmt.Errorf("add adjustment check: %w", err)
	}
	return nil
}
