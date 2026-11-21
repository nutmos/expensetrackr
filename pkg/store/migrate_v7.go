package store

import (
	"context"
	"database/sql"
	"fmt"
)

// Migration 6 -> 7: balances can be hidden.
//
// balances.hidden marks a balance the user no longer wants to see (a closed
// account, a paid-off loan) without deleting it: its transactions are kept and
// keep counting. Existing balances are visible (false).
func addBalanceHidden(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `ALTER TABLE balances ADD COLUMN IF NOT EXISTS hidden boolean NOT NULL DEFAULT false`); err != nil {
		return fmt.Errorf("add balances.hidden: %w", err)
	}
	return nil
}
