package store

import (
	"context"
	"database/sql"
	"fmt"
)

// Migration 2 -> 3: transactions may have type repay_debt.
//
// The baseline's inline CHECK on transactions.type (named
// transactions_type_check by Postgres) lists the allowed types. It is
// replaced by the same list plus 'repay_debt' (paying down a credit_card or
// other_liability from a payment_account; see transaction.RepayDebt).
// Existing rows all satisfy the wider check, so ADD CONSTRAINT validates
// without changing data.
func allowRepayDebtType(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `ALTER TABLE transactions
		DROP CONSTRAINT IF EXISTS transactions_type_check,
		ADD CONSTRAINT transactions_type_check CHECK (type IN ('expense', 'income', 'transfer', 'asset_purchase', 'return_on_investment', 'repay_debt', 'balance_adjustment'))`); err != nil {
		return fmt.Errorf("widen transactions_type_check: %w", err)
	}
	return nil
}
