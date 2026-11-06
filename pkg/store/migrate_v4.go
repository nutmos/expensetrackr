package store

import (
	"database/sql"
	"fmt"
)

// Migration 3 -> 4: transaction type return_on_investment.
//
// SQLite cannot alter the CHECK on transactions.type in place, so the table
// is rebuilt with the v3 definition plus 'return_on_investment' in the type
// list. Every other column, check and index is unchanged from v3; all rows
// are copied as-is.

const transactionsV4Table = `
CREATE TABLE transactions_v4 (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    uid           TEXT    NOT NULL UNIQUE CHECK (length(uid) = 36),
    user_uid      TEXT    NOT NULL REFERENCES users (uid) ON DELETE CASCADE,
    amount_minor  INTEGER NOT NULL CHECK (amount_minor > 0),
    amount_scale  INTEGER NOT NULL CHECK (amount_scale BETWEEN 0 AND 4),
    currency      TEXT    NOT NULL CHECK (length(currency) = 3),
    balance_uid   TEXT    NOT NULL CHECK (length(balance_uid) = 36),
    account       TEXT    NOT NULL CHECK (length(account) > 0),
    type          TEXT    NOT NULL DEFAULT 'expense' CHECK (type IN ('expense', 'income', 'transfer', 'asset_purchase', 'return_on_investment', 'balance_adjustment')),
    to_balance_uid TEXT   CHECK (to_balance_uid IS NULL OR length(to_balance_uid) = 36),
    to_account    TEXT,
    category_uid  TEXT    CHECK (category_uid IS NULL OR length(category_uid) = 36),
    spent_at      TEXT    NOT NULL,
    spent_at_unix INTEGER NOT NULL,
    note          TEXT    NOT NULL DEFAULT '',
    created_at    TEXT    NOT NULL,
    updated_at    TEXT,
    version       INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    balance_applied INTEGER NOT NULL DEFAULT 0 CHECK (balance_applied IN (0, 1)),
    adjustment_direction TEXT CHECK (adjustment_direction IS NULL OR adjustment_direction IN ('increase', 'decrease')),
    to_amount_minor INTEGER CHECK (to_amount_minor IS NULL OR to_amount_minor > 0),
    to_amount_scale INTEGER CHECK (to_amount_scale IS NULL OR to_amount_scale BETWEEN 0 AND 4),
    to_currency   TEXT    CHECK (to_currency IS NULL OR length(to_currency) = 3),
    exchange_rate TEXT,
    rate_source   TEXT    CHECK (rate_source IS NULL OR rate_source IN ('frankfurter', 'manual')),
    rate_date     TEXT    CHECK (rate_date IS NULL OR length(rate_date) = 10),
    import_ref    TEXT    CHECK (import_ref IS NULL OR length(import_ref) BETWEEN 1 AND 200),
    CHECK ((type = 'balance_adjustment') = (adjustment_direction IS NOT NULL)),
    CHECK (type <> 'balance_adjustment' OR (to_balance_uid IS NULL AND category_uid IS NULL AND balance_applied = 0))
);
`

func addReturnOnInvestmentType(tx *sql.Tx) error {
	if _, err := tx.Exec(transactionsV4Table); err != nil {
		return fmt.Errorf("create transactions_v4: %w", err)
	}
	if _, err := tx.Exec(`
		INSERT INTO transactions_v4 (
			id, uid, user_uid, amount_minor, amount_scale, currency, balance_uid, account, type,
			to_balance_uid, to_account, category_uid, spent_at, spent_at_unix, note,
			created_at, updated_at, version, balance_applied, adjustment_direction,
			to_amount_minor, to_amount_scale, to_currency, exchange_rate, rate_source, rate_date, import_ref
		)
		SELECT
			id, uid, user_uid, amount_minor, amount_scale, currency, balance_uid, account, type,
			to_balance_uid, to_account, category_uid, spent_at, spent_at_unix, note,
			created_at, updated_at, version, balance_applied, adjustment_direction,
			to_amount_minor, to_amount_scale, to_currency, exchange_rate, rate_source, rate_date, import_ref
		FROM transactions`); err != nil {
		return fmt.Errorf("copy transactions: %w", err)
	}
	if _, err := tx.Exec(`DROP TABLE transactions`); err != nil {
		return fmt.Errorf("drop transactions: %w", err)
	}
	if _, err := tx.Exec(`ALTER TABLE transactions_v4 RENAME TO transactions`); err != nil {
		return fmt.Errorf("rename transactions_v4: %w", err)
	}
	if err := syncAutoIncrement(tx, "transactions"); err != nil {
		return err
	}
	if _, err := tx.Exec(transactionsV3Indexes); err != nil {
		return fmt.Errorf("indexes for transactions: %w", err)
	}
	return nil
}
