package store

import (
	"context"
	"database/sql"
	"fmt"
)

// Migration 3 -> 4: optional foreign-currency amount on transactions.
//
// foreign_amount_minor / foreign_amount_scale / foreign_currency record what
// an expense, income, asset_purchase or return_on_investment was worth in
// another currency (see transaction.Transaction.ForeignAmount). They are
// informational only and never move a balance. The three columns are set
// together or not at all, and foreign_currency never equals currency.
// Existing rows get NULLs, which satisfy every new check.
func addForeignAmount(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `ALTER TABLE transactions
		ADD COLUMN foreign_amount_minor bigint   CHECK (foreign_amount_minor > 0),
		ADD COLUMN foreign_amount_scale smallint CHECK (foreign_amount_scale BETWEEN 0 AND 4),
		ADD COLUMN foreign_currency     char(3),
		ADD CONSTRAINT transactions_foreign_amount_check CHECK (
			(foreign_amount_minor IS NULL AND foreign_amount_scale IS NULL AND foreign_currency IS NULL)
			OR (foreign_amount_minor IS NOT NULL AND foreign_amount_scale IS NOT NULL AND foreign_currency IS NOT NULL
			    AND foreign_currency <> currency
			    AND type IN ('expense', 'income', 'asset_purchase', 'return_on_investment')))`); err != nil {
		return fmt.Errorf("add foreign amount columns: %w", err)
	}
	return nil
}
