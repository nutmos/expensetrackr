package store

import (
	"context"
	"database/sql"
	"fmt"
)

// Migration 8 -> 9: transaction type beginning_balance (issue #172).
//
// A beginning_balance row records the amount a balance started with. Every
// balance has exactly one (unique index transactions_beginning_balance_uq);
// the store writes it when the balance is created. Like a balance_adjustment
// it has an adjustment_direction (decrease = a starting amount below zero),
// no destination and no category, and unlike any other row its amount may be
// 0 (transactions_amount_minor_check is replaced).
//
// Existing balances get theirs here: the starting amount is the current
// amount minus everything the applied transactions did to it (the same sign
// rules as transaction.Effects; rows recorded in a currency other than the
// balance's are left out, as the store never applies those), dated at the
// balance's earliest transaction (its spent_at as entered), or at the
// balance's created_at (UTC) when it has none.
func addBeginningBalance(ctx context.Context, tx *sql.Tx) error {
	// The baseline's unnamed checks: the column check on amount_minor and the
	// table check tying adjustment_direction to balance_adjustment.
	var dirCheck string
	if err := tx.QueryRowContext(ctx, `SELECT conname FROM pg_constraint
		WHERE conrelid = 'transactions'::regclass AND contype = 'c'
		  AND pg_get_constraintdef(oid) LIKE '%balance_adjustment%' AND pg_get_constraintdef(oid) LIKE '%adjustment_direction IS NOT NULL%'`).Scan(&dirCheck); err != nil {
		return fmt.Errorf("find adjustment direction check: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE transactions
		DROP CONSTRAINT IF EXISTS transactions_type_check,
		ADD CONSTRAINT transactions_type_check CHECK (type IN ('expense', 'income', 'transfer', 'asset_purchase', 'return_on_investment', 'asset_sale', 'repay_debt', 'balance_adjustment', 'beginning_balance')),
		DROP CONSTRAINT IF EXISTS transactions_amount_minor_check,
		ADD CONSTRAINT transactions_amount_minor_check CHECK (amount_minor > 0 OR (type = 'beginning_balance' AND amount_minor = 0)),
		DROP CONSTRAINT `+quoteIdent(dirCheck)+`,
		ADD CONSTRAINT transactions_direction_check CHECK ((type IN ('balance_adjustment', 'beginning_balance')) = (adjustment_direction IS NOT NULL)),
		ADD CONSTRAINT transactions_beginning_balance_check CHECK (
			type <> 'beginning_balance' OR (to_balance_uid IS NULL AND category_uid IS NULL AND import_ref IS NULL))`); err != nil {
		return fmt.Errorf("add beginning balance type: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE UNIQUE INDEX transactions_beginning_balance_uq ON transactions (balance_uid) WHERE type = 'beginning_balance'`); err != nil {
		return fmt.Errorf("add beginning balance index: %w", err)
	}
	if _, err := tx.ExecContext(ctx, backfillBeginningBalances); err != nil {
		return fmt.Errorf("backfill beginning balances: %w", err)
	}
	return nil
}

// backfillBeginningBalances inserts the beginning_balance of every existing
// balance (see addBeginningBalance).
const backfillBeginningBalances = `
WITH effects AS (
	-- balance_uid side: value_delta is the change in value (asset balance up,
	-- liability debt down); amount_delta is a change of the amount as shown
	-- (balance adjustments only).
	SELECT t.balance_uid AS buid, t.currency AS cur,
		CASE t.type
			WHEN 'income' THEN t.amount_minor
			WHEN 'return_on_investment' THEN t.amount_minor
			WHEN 'asset_sale' THEN t.amount_minor + COALESCE(t.gain_loss_minor, 0)
			WHEN 'transfer' THEN -(t.amount_minor + COALESCE(t.interest_amount_minor, 0) + COALESCE(t.fee_amount_minor, 0))
			WHEN 'asset_purchase' THEN -(t.amount_minor + COALESCE(t.interest_amount_minor, 0) + COALESCE(t.fee_amount_minor, 0))
			WHEN 'repay_debt' THEN -(t.amount_minor + COALESCE(t.interest_amount_minor, 0) + COALESCE(t.fee_amount_minor, 0))
			WHEN 'balance_adjustment' THEN 0
			ELSE -t.amount_minor
		END::numeric AS value_delta,
		CASE WHEN t.type = 'balance_adjustment' THEN
			CASE WHEN t.adjustment_direction = 'decrease' THEN -t.amount_minor ELSE t.amount_minor END
		ELSE 0 END::numeric AS amount_delta
	FROM transactions t WHERE t.balance_applied
	UNION ALL
	-- to_balance_uid side (a return on investment does not move its asset).
	SELECT t.to_balance_uid, COALESCE(t.to_currency, t.currency),
		CASE t.type
			WHEN 'asset_sale' THEN -t.amount_minor
			WHEN 'return_on_investment' THEN 0
			ELSE COALESCE(t.to_amount_minor, t.amount_minor)
		END::numeric, 0::numeric
	FROM transactions t WHERE t.balance_applied AND t.to_balance_uid IS NOT NULL
),
sums AS (
	SELECT e.buid, SUM(e.value_delta) AS value_delta, SUM(e.amount_delta) AS amount_delta
	FROM effects e JOIN balances b ON b.uid = e.buid AND b.currency = e.cur
	GROUP BY e.buid
),
firsts AS (
	SELECT DISTINCT ON (buid) buid, spent_at, spent_at_utc FROM (
		SELECT balance_uid AS buid, spent_at, spent_at_utc, id FROM transactions
		UNION ALL
		SELECT to_balance_uid, spent_at, spent_at_utc, id FROM transactions WHERE to_balance_uid IS NOT NULL
	) x ORDER BY buid, spent_at_utc, id
),
starts AS (
	SELECT b.id, b.uid, b.user_uid, b.name, b.currency, b.amount_scale, b.created_at, f.spent_at, f.spent_at_utc,
		CASE WHEN b.type IN ('credit_card', 'other_liability')
			THEN b.debt_minor::numeric + COALESCE(s.value_delta, 0) - COALESCE(s.amount_delta, 0)
			ELSE b.balance_minor::numeric - COALESCE(s.value_delta, 0) - COALESCE(s.amount_delta, 0)
		END AS start
	FROM balances b LEFT JOIN sums s ON s.buid = b.uid LEFT JOIN firsts f ON f.buid = b.uid
)
INSERT INTO transactions (uid, user_uid, type, amount_minor, amount_scale, currency, balance_uid, account, spent_at, spent_at_utc, note, created_at, version, balance_applied, adjustment_direction)
SELECT gen_random_uuid(), st.user_uid, 'beginning_balance', abs(st.start)::bigint, st.amount_scale, st.currency, st.uid, st.name,
	COALESCE(st.spent_at, to_char(st.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')),
	COALESCE(st.spent_at_utc, date_trunc('second', st.created_at)),
	'', st.created_at, 1, true,
	CASE WHEN st.start < 0 THEN 'decrease' ELSE 'increase' END
FROM starts st ORDER BY st.id`

// quoteIdent quotes a Postgres identifier.
func quoteIdent(s string) string {
	out := `"`
	for _, r := range s {
		if r == '"' {
			out += `""`
		} else {
			out += string(r)
		}
	}
	return out + `"`
}
