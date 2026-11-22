package store

import (
	"context"
	"database/sql"
	"fmt"
)

// Migration 7 -> 8: transaction type asset_sale.
//
// transactions_type_check is widened with 'asset_sale' (selling all or part
// of an other_asset; see transaction.AssetSale). Two columns record the sale:
//
//   - gain_loss_minor: the signed gain (> 0) or loss (< 0) of the sale in the
//     transaction currency at amount_scale; NULL when neither. amount_minor
//     is the principal, and the proceeds (amount_minor + gain_loss_minor)
//     are never negative.
//   - entire_asset_sale: the whole asset was sold (the store hid it when the
//     transaction was saved).
//
// Both are set only on an asset_sale, which may carry a category (of its
// gain or loss) only when it has a gain or loss. No existing row is an
// asset_sale, so every new check holds without changing data.
func addAssetSale(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `ALTER TABLE transactions
		DROP CONSTRAINT IF EXISTS transactions_type_check,
		ADD CONSTRAINT transactions_type_check CHECK (type IN ('expense', 'income', 'transfer', 'asset_purchase', 'return_on_investment', 'asset_sale', 'repay_debt', 'balance_adjustment')),
		ADD COLUMN gain_loss_minor   bigint  CHECK (gain_loss_minor <> 0),
		ADD COLUMN entire_asset_sale boolean NOT NULL DEFAULT false,
		ADD CONSTRAINT transactions_sale_result_check CHECK (
			type = 'asset_sale' OR (gain_loss_minor IS NULL AND NOT entire_asset_sale)),
		ADD CONSTRAINT transactions_sale_proceeds_check CHECK (
			gain_loss_minor IS NULL OR amount_minor + gain_loss_minor >= 0),
		ADD CONSTRAINT transactions_sale_category_check CHECK (
			type <> 'asset_sale' OR category_uid IS NULL OR gain_loss_minor IS NOT NULL)`); err != nil {
		return fmt.Errorf("add asset sale: %w", err)
	}
	return nil
}
