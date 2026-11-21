package store

import (
	"context"
	"database/sql"
	"fmt"
)

// Migration 4 -> 5: optional interest and fees on debt repayments.
//
// interest_amount_minor / fee_amount_minor record the interest and other
// fees paid together with a repay_debt, in the transaction currency at
// amount_scale (see transaction.Transaction.InterestAmount). amount_minor
// stays the principal; the payment account pays all three. They are set only
// on a repay_debt and are positive when set. A repay_debt may now carry a
// category (the expense category of its charges) only when it has charges.
// Existing rows get NULLs, and no repay_debt has a category yet, so every
// new check holds without changing data.
func addRepayDebtCharges(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `ALTER TABLE transactions
		ADD COLUMN interest_amount_minor bigint CHECK (interest_amount_minor > 0),
		ADD COLUMN fee_amount_minor      bigint CHECK (fee_amount_minor > 0),
		ADD CONSTRAINT transactions_repay_charges_check CHECK (
			type = 'repay_debt'
			OR (interest_amount_minor IS NULL AND fee_amount_minor IS NULL)),
		ADD CONSTRAINT transactions_repay_category_check CHECK (
			type <> 'repay_debt' OR category_uid IS NULL
			OR interest_amount_minor IS NOT NULL OR fee_amount_minor IS NOT NULL)`); err != nil {
		return fmt.Errorf("add repay debt charge columns: %w", err)
	}
	return nil
}
