package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/nutmos/expensetrackr/pkg/common_type"
	"github.com/nutmos/expensetrackr/pkg/money"
	"github.com/nutmos/expensetrackr/pkg/transaction"
)

// Create inserts a validated transaction as a new row in transactions owned by
// userUID, fills in UID, ID, CreatedAt and Version (1), and moves the
// referenced balances (see applyBalanceEffects), all in one database
// transaction. A referenced balance that is missing, owned by another user, or
// in another currency is a *validate.ValidationError and nothing is written.
func (s *Store) Create(ctx context.Context, userUID string, e *transaction.Transaction) error {
	if e.Type == transaction.BalanceAdjustment {
		return ErrReadOnly
	}
	if err := checkFX(*e); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("insert transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit
	next, err := insertTx(ctx, tx, userUID, *e, s.nowRFC3339())
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("insert transaction: %w", err)
	}
	*e = next
	return nil
}

// insertTx inserts e (already validated, not a balance adjustment) inside tx
// and applies its balance effects. A duplicate (balance_uid, import_ref) is a
// *DuplicateImportRefError.
func insertTx(ctx context.Context, tx *sql.Tx, userUID string, e transaction.Transaction, now string) (transaction.Transaction, error) {
	scale, ok := money.MinorUnits(e.Currency)
	if !ok {
		return e, fmt.Errorf("unknown currency %q", e.Currency)
	}
	uid, err := common_type.NewUID()
	if err != nil {
		return e, err
	}
	next := e
	next.UID, next.CreatedAt, next.UpdatedAt, next.Version, next.AdjustsBalances = uid, now, nil, 1, true
	res, err := tx.ExecContext(ctx,
		`INSERT INTO transactions (uid, user_uid, type, amount_minor, amount_scale, currency, balance_uid, account, to_balance_uid, to_account, category_uid, spent_at, spent_at_unix, note, created_at, version, balance_applied,
		                           to_amount_minor, to_amount_scale, to_currency, exchange_rate, rate_source, rate_date, import_ref)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, 1, ?, ?, ?, ?, ?, ?, ?)`,
		append(append([]any{next.UID, userUID, string(next.Type), next.AmountMinor, scale, next.Currency, next.BalanceUID, next.Account, next.ToBalanceUID, next.ToAccount, next.CategoryUID, next.SpentAt, next.SpentTime.Unix(), next.Note, next.CreatedAt},
			fxArgs(next)...), next.ImportRef)...)
	if err != nil {
		return e, mapImportRefErr(ctx, tx, next, fmt.Errorf("insert transaction: %w", err))
	}
	id, err := res.LastInsertId()
	if err != nil {
		return e, fmt.Errorf("insert transaction: %w", err)
	}
	next.ID = id
	if err := applyBalanceEffects(ctx, tx, userUID, nil, &next, now); err != nil {
		return e, err
	}
	return next, nil
}

// ListFilter restricts List results. Zero values mean "no restriction".
type ListFilter struct {
	From        *time.Time       // inclusive
	To          *time.Time       // inclusive
	Type        transaction.Type // "" = all
	CategoryUID common_type.UID  // "" = all
	Limit       int
}

const selectCols = `id, uid, type, amount_minor, amount_scale, currency, balance_uid, account, to_balance_uid, to_account, category_uid, spent_at, note, created_at, updated_at, version, balance_applied, adjustment_direction,
	to_amount_minor, to_amount_scale, to_currency, exchange_rate, rate_source, rate_date, import_ref`

// List returns rows from transactions owned by userUID, newest first (by spend
// time, then ID).
func (s *Store) List(ctx context.Context, userUID string, f ListFilter) ([]transaction.Transaction, error) {
	query := `SELECT ` + selectCols + ` FROM transactions WHERE user_uid = ?`
	args := []any{userUID}
	if f.From != nil {
		query += ` AND spent_at_unix >= ?`
		args = append(args, f.From.Unix())
	}
	if f.To != nil {
		query += ` AND spent_at_unix <= ?`
		args = append(args, f.To.Unix())
	}
	if f.CategoryUID != "" {
		query += ` AND category_uid = ?`
		args = append(args, f.CategoryUID)
	}
	if f.Type != "" {
		query += ` AND type = ?`
		args = append(args, string(f.Type))
	}
	query += ` ORDER BY spent_at_unix DESC, id DESC`
	if f.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, f.Limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list transactions: %w", err)
	}
	defer rows.Close()

	out := []transaction.Transaction{}
	for rows.Next() {
		e, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Get returns one row from transactions owned by userUID or ErrNotFound
// (including when the id belongs to another user).
func (s *Store) Get(ctx context.Context, userUID string, id int64) (transaction.Transaction, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+selectCols+` FROM transactions WHERE id = ? AND user_uid = ?`, id, userUID)
	e, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return transaction.Transaction{}, ErrNotFound
	}
	return e, err
}

// GetByUID returns one row from transactions by its UUID for userUID or
// ErrNotFound (including when the uid belongs to another user).
func (s *Store) GetByUID(ctx context.Context, userUID string, uid common_type.UID) (transaction.Transaction, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+selectCols+` FROM transactions WHERE uid = ? AND user_uid = ?`, uid, userUID)
	e, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return transaction.Transaction{}, ErrNotFound
	}
	return e, err
}

// UpdateWith loads row id from transactions, passes it to fn and stores the
// transaction fn returns, all inside one database transaction (so a PUT's
// read-modify-write is atomic). fn receives the current row and returns the
// new, already validated values; an error from fn aborts the update and is
// returned unchanged. ID, UID and CreatedAt are preserved, UpdatedAt is set
// to now (UTC) and Version is incremented.
//
// If opts.Version > 0 and differs from the stored version, ErrVersionConflict
// is returned. The old balance effect is reversed and the new one applied in
// the same database transaction (amount, type and account changes all work);
// a new balance that is missing or in another currency is a
// *validate.ValidationError. Returns ErrNotFound if the row does not exist.
func (s *Store) UpdateWith(ctx context.Context, userUID string, id int64, opts WriteOptions, fn func(cur transaction.Transaction) (transaction.Transaction, error)) (transaction.Transaction, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return transaction.Transaction{}, fmt.Errorf("update transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit

	cur, err := scan(tx.QueryRowContext(ctx, `SELECT `+selectCols+` FROM transactions WHERE id = ? AND user_uid = ?`, id, userUID))
	if errors.Is(err, sql.ErrNoRows) {
		return transaction.Transaction{}, ErrNotFound
	}
	if err != nil {
		return transaction.Transaction{}, err
	}
	if cur.Type == transaction.BalanceAdjustment {
		return transaction.Transaction{}, ErrReadOnly
	}
	if opts.Version > 0 && cur.Version != opts.Version {
		return transaction.Transaction{}, ErrVersionConflict
	}
	next, err := fn(cur)
	if err != nil {
		return transaction.Transaction{}, err
	}
	if next.Type == transaction.BalanceAdjustment {
		return transaction.Transaction{}, ErrReadOnly
	}
	next.AdjustmentDirection = nil
	if err := checkFX(next); err != nil {
		return transaction.Transaction{}, err
	}
	scale, ok := money.MinorUnits(next.Currency)
	if !ok {
		return transaction.Transaction{}, fmt.Errorf("unknown currency %q", next.Currency)
	}
	now := s.nowRFC3339()
	next.ID, next.UID, next.CreatedAt, next.UpdatedAt = cur.ID, cur.UID, cur.CreatedAt, &now
	next.Version, next.AdjustsBalances = cur.Version+1, cur.AdjustsBalances

	res, err := tx.ExecContext(ctx,
		`UPDATE transactions SET type = ?, amount_minor = ?, amount_scale = ?, currency = ?, balance_uid = ?, account = ?, to_balance_uid = ?, to_account = ?, category_uid = ?,
		        spent_at = ?, spent_at_unix = ?, note = ?, updated_at = ?, version = ?,
		        to_amount_minor = ?, to_amount_scale = ?, to_currency = ?, exchange_rate = ?, rate_source = ?, rate_date = ?, import_ref = ?
		 WHERE id = ? AND version = ?`,
		append(append([]any{string(next.Type), next.AmountMinor, scale, next.Currency, next.BalanceUID, next.Account, next.ToBalanceUID, next.ToAccount, next.CategoryUID,
			next.SpentAt, next.SpentTime.Unix(), next.Note, now, next.Version}, fxArgs(next)...), next.ImportRef, id, cur.Version)...)
	if err != nil {
		return transaction.Transaction{}, mapImportRefErr(ctx, tx, next, fmt.Errorf("update transaction: %w", err))
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return transaction.Transaction{}, ErrVersionConflict
	}
	if err := applyBalanceEffects(ctx, tx, userUID, &cur, &next, now); err != nil {
		return transaction.Transaction{}, err
	}
	if err := tx.Commit(); err != nil {
		return transaction.Transaction{}, fmt.Errorf("update transaction: %w", err)
	}
	return next, nil
}

// DeleteWith removes one row from transactions owned by userUID and reverses
// its balance effect, in one database transaction. Returns ErrNotFound
// (including when the id belongs to another user), or ErrVersionConflict when
// opts.Version > 0 and differs from the stored one.
func (s *Store) DeleteWith(ctx context.Context, userUID string, id int64, opts WriteOptions) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("delete transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	cur, err := scan(tx.QueryRowContext(ctx, `SELECT `+selectCols+` FROM transactions WHERE id = ? AND user_uid = ?`, id, userUID))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if cur.Type == transaction.BalanceAdjustment {
		return ErrReadOnly
	}
	if opts.Version > 0 && cur.Version != opts.Version {
		return ErrVersionConflict
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM transactions WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete transaction: %w", err)
	}
	if err := applyBalanceEffects(ctx, tx, userUID, &cur, nil, s.nowRFC3339()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("delete transaction: %w", err)
	}
	return nil
}

// checkFX enforces the cross-currency invariants: the to_* fields are set
// together, only on a transfer, and only when to_currency differs.
func checkFX(e transaction.Transaction) error {
	if e.ToCurrency == nil && e.ToAmountMinor == nil {
		if e.ExchangeRate != nil || e.RateSource != nil || e.RateDate != nil {
			return errors.New("exchange_rate/rate_source/rate_date set without to_amount")
		}
		return nil
	}
	if e.Type != transaction.Transfer || e.ToCurrency == nil || e.ToAmountMinor == nil || *e.ToAmountMinor <= 0 ||
		e.ExchangeRate == nil || e.RateSource == nil || *e.ToCurrency == e.Currency {
		return fmt.Errorf("invalid cross-currency fields on a %s transaction", e.Type)
	}
	if _, ok := money.MinorUnits(*e.ToCurrency); !ok {
		return fmt.Errorf("unknown currency %q", *e.ToCurrency)
	}
	return nil
}

// fxArgs are the values of the to_amount_minor ... rate_date columns.
func fxArgs(e transaction.Transaction) []any {
	if e.ToCurrency == nil {
		return []any{nil, nil, nil, nil, nil, nil}
	}
	scale, _ := money.MinorUnits(*e.ToCurrency)
	return []any{*e.ToAmountMinor, scale, *e.ToCurrency, *e.ExchangeRate, *e.RateSource, e.RateDate}
}

func scan(r scanner) (transaction.Transaction, error) {
	var e transaction.Transaction
	var scale int
	var updated, toUID, toName, catUID, dir sql.NullString
	var typ string
	var toMinor, toScale sql.NullInt64
	var toCur, rate, rateSrc, rateDate, importRef sql.NullString
	if err := r.Scan(&e.ID, &e.UID, &typ, &e.AmountMinor, &scale, &e.Currency, &e.BalanceUID, &e.Account, &toUID, &toName, &catUID, &e.SpentAt, &e.Note, &e.CreatedAt, &updated,
		&e.Version, &e.AdjustsBalances, &dir, &toMinor, &toScale, &toCur, &rate, &rateSrc, &rateDate, &importRef); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return e, err
		}
		return e, fmt.Errorf("scan transaction: %w", err)
	}
	e.Amount = money.FormatAmount(e.AmountMinor, scale)
	if updated.Valid {
		e.UpdatedAt = &updated.String
	}
	e.Type = transaction.Type(typ)
	if toUID.Valid {
		to := transaction.UID(toUID.String)
		e.ToBalanceUID = &to
	}
	if toName.Valid {
		e.ToAccount = &toName.String
	}
	if catUID.Valid {
		cat := transaction.UID(catUID.String)
		e.CategoryUID = &cat
	}
	if dir.Valid {
		d := transaction.Direction(dir.String)
		e.AdjustmentDirection = &d
	}
	if toMinor.Valid && toCur.Valid {
		m, a, c := toMinor.Int64, money.FormatAmount(toMinor.Int64, int(toScale.Int64)), toCur.String
		e.ToAmountMinor, e.ToAmount, e.ToCurrency = &m, &a, &c
		if rate.Valid {
			e.ExchangeRate = &rate.String
		}
		if rateSrc.Valid {
			e.RateSource = &rateSrc.String
		}
		if rateDate.Valid {
			e.RateDate = &rateDate.String
		}
	}
	if importRef.Valid {
		e.ImportRef = &importRef.String
	}
	if t, err := time.Parse(time.RFC3339, e.SpentAt); err == nil {
		e.SpentTime = t
	}
	return e, nil
}
