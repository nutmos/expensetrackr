package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/nutmos/expensetrackr/pkg/balance"
	"github.com/nutmos/expensetrackr/pkg/common_type"
	"github.com/nutmos/expensetrackr/pkg/transaction"
	"github.com/nutmos/expensetrackr/pkg/validate"
)

// Beginning balances (issue #172): every balance has exactly one
// beginning_balance transaction, recorded with the balance, holding the
// amount it started with. It is always the balance's earliest transaction:
// a transaction dated before it moves it back (pullBeginningBalances), and
// it cannot itself be dated after another transaction of its balance
// (checkBeginningBalanceDate).

// startOf is the amount b starts with as a beginning balance: the shown
// amount (balance of an asset, debt of a liability) as a non-negative
// amount and a direction (decrease = below zero). A missing amount is 0.
func startOf(b balance.Balance) (int64, transaction.Direction) {
	p := b.BalanceMinor
	if b.Type.IsLiability() {
		p = b.DebtMinor
	}
	var v int64
	if p != nil {
		v = *p
	}
	if v < 0 {
		return -v, transaction.Decrease // -math.MinInt64 is not reachable: amounts have at most 13+4 digits
	}
	return v, transaction.Increase
}

// recordBeginningBalance inserts the beginning_balance of the new balance b
// inside the caller's database transaction. b already holds the amount, so
// the row is stored as applied without moving b again (like a balance
// adjustment). spentAt comes from the store clock (with its local UTC offset).
func (s *Store) recordBeginningBalance(ctx context.Context, tx *sql.Tx, userUID string, b balance.Balance, createdAt string, spentAt time.Time) error {
	uid, err := common_type.NewUID()
	if err != nil {
		return err
	}
	amt, dir := startOf(b)
	at := spentAt.Truncate(time.Second)
	name, err := s.seal(b.Name)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO transactions (uid, user_uid, type, amount_minor, amount_scale, currency, balance_uid, account, spent_at, spent_at_utc, note, created_at, version, balance_applied, adjustment_direction)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, '', $11, 1, true, $12)`,
		uid, userUID, string(transaction.BeginningBalance), amt, b.Scale, b.Currency, b.UID, name,
		at.Format(time.RFC3339), at.UTC(), createdAt, string(dir)); err != nil {
		return fmt.Errorf("record beginning balance: %w", err)
	}
	return nil
}

// resetBeginningBalance rewrites the beginning balance of b after a currency
// change (only possible while it is b's only transaction): it takes b's new
// currency and its new amount, which is all b has ever held.
func (s *Store) resetBeginningBalance(ctx context.Context, tx *sql.Tx, userUID string, b balance.Balance, now string) error {
	amt, dir := startOf(b)
	name, err := s.seal(b.Name)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE transactions SET amount_minor = $1, amount_scale = $2, currency = $3, adjustment_direction = $4, account = $5,
		        updated_at = $6, version = version + 1
		 WHERE user_uid = $7 AND balance_uid = $8 AND type = $9`,
		amt, b.Scale, b.Currency, string(dir), name, now, userUID, b.UID, string(transaction.BeginningBalance)); err != nil {
		return fmt.Errorf("reset beginning balance: %w", err)
	}
	return nil
}

// pullBeginningBalances moves the beginning balance of every balance e
// references (balance_uid, to_balance_uid) back to e's date and time when e
// is dated earlier (version +1). e must be a transaction other than a
// beginning balance.
func pullBeginningBalances(ctx context.Context, tx *sql.Tx, userUID string, e transaction.Transaction, now string) error {
	if e.Type == transaction.BeginningBalance {
		return nil
	}
	uids := []string{string(e.BalanceUID)}
	if e.ToBalanceUID != nil {
		uids = append(uids, string(*e.ToBalanceUID))
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE transactions SET spent_at = $1, spent_at_utc = $2, updated_at = $3, version = version + 1
		 WHERE user_uid = $4 AND type = $5 AND balance_uid = ANY($6::uuid[]) AND spent_at_utc > $2`,
		e.SpentAt, spentUTC(e), now, userUID, string(transaction.BeginningBalance), uids); err != nil {
		return fmt.Errorf("move beginning balance: %w", err)
	}
	return nil
}

// checkBeginningBalanceDate checks that the edited beginning balance next
// (row id) is not dated after any other transaction of its balance; a
// violation is a *validate.ValidationError on spent_at.
func checkBeginningBalanceDate(ctx context.Context, tx *sql.Tx, userUID string, id int64, next transaction.Transaction) error {
	var first string
	var firstUTC time.Time
	err := tx.QueryRowContext(ctx,
		`SELECT spent_at, spent_at_utc FROM transactions
		 WHERE user_uid = $1 AND (balance_uid = $2 OR to_balance_uid = $2) AND id <> $3
		 ORDER BY spent_at_utc, id LIMIT 1`, userUID, next.BalanceUID, id).Scan(&first, &firstUTC)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check beginning balance date: %w", err)
	}
	if !spentUTC(next).After(firstUTC) {
		return nil
	}
	return &validate.ValidationError{Fields: map[string]string{
		"spent_at": fmt.Sprintf("must not be later than the balance's earliest transaction (%s): the beginning balance comes first", first),
	}}
}
