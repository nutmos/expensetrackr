package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/nutmos/expensetrackr/pkg/balance"
	"github.com/nutmos/expensetrackr/pkg/money"
	"github.com/nutmos/expensetrackr/pkg/transaction"
)

// clock is the time source for balance adjustment timestamps (tests may
// replace it).
var clock = time.Now

// adjustment describes the Balance Adjustment implied by a manual edit.
type adjustment struct {
	field     string // "balance" (asset) or "debt" (liability)
	old, new  int64  // minor units at the balance's scale
	amount    int64  // |new - old| > 0
	direction transaction.Direction
}

// adjustmentFor returns the adjustment a manual edit from cur to next implies,
// or ok=false when there is none:
//
//   - the edited amount is balance for an asset and debt for a liability; a
//     limit change is not money moving and is never an adjustment;
//   - no change in that amount (e.g. a rename or a limit-only edit): none;
//   - a currency change in the same edit: none (the old and new amounts are in
//     different currencies; this is only possible while no transaction
//     references the balance, see ErrBalanceInUse).
//
// A missing amount counts as 0.
func adjustmentFor(cur, next balance.Balance) (adjustment, bool) {
	if cur.Currency != next.Currency || cur.Scale != next.Scale {
		return adjustment{}, false
	}
	a := adjustment{field: "balance"}
	oldP, newP := cur.BalanceMinor, next.BalanceMinor
	if cur.Type.IsLiability() {
		a.field, oldP, newP = "debt", cur.DebtMinor, next.DebtMinor
	}
	if oldP != nil {
		a.old = *oldP
	}
	if newP != nil {
		a.new = *newP
	}
	switch {
	case a.new > a.old:
		a.amount, a.direction = a.new-a.old, transaction.Increase
	case a.new < a.old:
		a.amount, a.direction = a.old-a.new, transaction.Decrease
	default:
		return adjustment{}, false
	}
	return a, true
}

// recordBalanceAdjustment inserts the balance_adjustment transaction for a
// manual edit from cur to next (if any), inside the caller's database
// transaction. The row is never applied to the balance (balance_applied = 0,
// and transaction.Effects returns nothing for it): the edit already set the
// new value. spent_at is the server's current local time with its UTC offset.
func recordBalanceAdjustment(ctx context.Context, tx *sql.Tx, cur, next balance.Balance, createdAt string) error {
	a, ok := adjustmentFor(cur, next)
	if !ok {
		return nil
	}
	uid, err := newUID()
	if err != nil {
		return err
	}
	at := clock().Truncate(time.Second)
	note := fmt.Sprintf("Manual edit of %s: %s → %s", a.field,
		money.FormatAmount(a.old, next.Scale), money.FormatAmount(a.new, next.Scale))
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO transactions (uid, type, amount_minor, amount_scale, currency, balance_uid, account, spent_at, spent_at_unix, note, created_at, version, balance_applied, adjustment_direction)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, 0, ?)`,
		uid, string(transaction.BalanceAdjustment), a.amount, next.Scale, next.Currency, cur.UID, next.Name,
		at.Format(time.RFC3339), at.Unix(), note, createdAt, string(a.direction)); err != nil {
		return fmt.Errorf("record balance adjustment: %w", err)
	}
	return nil
}
