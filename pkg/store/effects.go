package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/nutmos/expensetrackr/pkg/balance"
	"github.com/nutmos/expensetrackr/pkg/transaction"
	"github.com/nutmos/expensetrackr/pkg/validate"
)

// applyBalanceEffects moves balances for a transaction write, inside the
// caller's database transaction tx:
//
//   - old (update/delete): its effect is reversed, if it ever applied one
//     (old.AdjustsBalances; rows recorded before schema v11 did not).
//   - next (create/update): its effect is applied (if next.AdjustsBalances).
//     Every balance it references must exist and use the transaction's
//     currency, else a *validate.ValidationError on balance_uid /
//     to_balance_uid is returned. A cross-currency transfer moves the
//     destination by to_amount in to_currency (see docs/exchange-rates.md);
//     reversal always uses the stored values, never a fresh rate.
//
// Deltas are summed per balance, so each touched balance is written once
// (version +1, updated_at = now); a balance whose net change is zero (e.g. an
// edit of the note only) is not written at all. A reversed balance that no
// longer exists (deleted) is skipped. See transaction.Effects for the sign
// rules.
func applyBalanceEffects(ctx context.Context, tx *sql.Tx, old, next *transaction.Transaction, now string) error {
	deltas := map[string]int64{}
	var order []string
	reversedCurrency := map[string]string{}
	add := func(uid string, d int64) {
		if _, ok := deltas[uid]; !ok {
			order = append(order, uid)
		}
		deltas[uid] += d // |amount| < 10^17, so a sum of two never overflows
	}
	if old != nil && old.AdjustsBalances {
		for _, ef := range old.Effects() {
			add(ef.BalanceUID, -ef.Delta)
			reversedCurrency[ef.BalanceUID] = effectCurrency(*old, ef)
		}
	}
	if next != nil && next.AdjustsBalances {
		for _, ef := range next.Effects() {
			b, err := balanceByUIDTx(ctx, tx, ef.BalanceUID)
			if errors.Is(err, ErrNotFound) {
				return &validate.ValidationError{Fields: map[string]string{ef.Field: "does not match any balance"}}
			}
			if err != nil {
				return err
			}
			if want := effectCurrency(*next, ef); b.Currency != want {
				msg := fmt.Sprintf("balance %q is in %s but the transaction is in %s; the currencies must match", b.Name, b.Currency, want)
				if ef.Field == "to_balance_uid" && next.Type == transaction.Transfer {
					msg = fmt.Sprintf("balance %q is in %s but to_currency is %s", b.Name, b.Currency, want)
				}
				return &validate.ValidationError{Fields: map[string]string{ef.Field: msg}}
			}
			add(ef.BalanceUID, ef.Delta)
		}
	}
	for _, uid := range order {
		d := deltas[uid]
		if d == 0 {
			continue
		}
		b, err := balanceByUIDTx(ctx, tx, uid)
		if errors.Is(err, ErrNotFound) {
			continue // balance deleted since: nothing left to adjust
		}
		if err != nil {
			return err
		}
		if cur, ok := reversedCurrency[uid]; ok && cur != b.Currency {
			// Prevented by UpdateBalanceWith (currency is fixed while
			// transactions reference a balance); never mix minor units.
			return fmt.Errorf("balance %s is in %s but its transaction was recorded in %s; refusing to adjust", uid, b.Currency, cur)
		}
		if err := b.ApplyValueDelta(d); err != nil {
			if errors.Is(err, balance.ErrOverflow) {
				return &validate.ValidationError{Fields: map[string]string{"amount": fmt.Sprintf("would put balance %q out of range", b.Name)}}
			}
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE balances SET balance_minor = ?, debt_minor = ?, version = version + 1, updated_at = ? WHERE id = ?`,
			nullInt(b.BalanceMinor), nullInt(b.DebtMinor), now, b.ID); err != nil {
			return fmt.Errorf("adjust balance %s: %w", uid, err)
		}
	}
	return nil
}

func effectCurrency(e transaction.Transaction, ef transaction.Effect) string {
	if ef.Currency != "" {
		return ef.Currency
	}
	return e.Currency
}

func balanceByUIDTx(ctx context.Context, tx *sql.Tx, uid string) (balance.Balance, error) {
	b, err := scanBalance(tx.QueryRowContext(ctx, `SELECT `+balanceCols+` FROM balances WHERE uid = ?`, uid))
	if errors.Is(err, sql.ErrNoRows) {
		return balance.Balance{}, ErrNotFound
	}
	return b, err
}
