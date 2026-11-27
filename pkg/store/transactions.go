package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/nutmos/expensetrackr/pkg/common_type"
	"github.com/nutmos/expensetrackr/pkg/money"
	"github.com/nutmos/expensetrackr/pkg/transaction"
	"github.com/nutmos/expensetrackr/pkg/validate"
)

// Create inserts a validated transaction as a new row in transactions owned by
// userUID, fills in UID, ID, CreatedAt and Version (1), and moves the
// referenced balances (see applyBalanceEffects), all in one database
// transaction. A referenced balance that is missing, owned by another user, or
// in another currency is a *validate.ValidationError and nothing is written.
func (s *Store) Create(ctx context.Context, userUID string, e *transaction.Transaction) error {
	if e.Type.ServerRecorded() {
		return ErrReadOnly
	}
	if err := checkFX(*e); err != nil {
		return err
	}
	if err := checkForeign(*e); err != nil {
		return err
	}
	if err := checkCharges(*e); err != nil {
		return err
	}
	if err := checkSaleResult(*e); err != nil {
		return err
	}
	var next transaction.Transaction
	err := s.inTx(ctx, "insert transaction", func(tx *sql.Tx) error {
		var err error
		next, err = s.insertTx(ctx, tx, userUID, *e, s.nowRFC3339())
		return err
	})
	if err != nil {
		return err
	}
	*e = next
	return nil
}

// insertTx inserts e (already validated, not a server-recorded type) inside
// tx, applies its balance effects and moves the beginning balances of its
// balances back to its date when it is earlier. A duplicate (balance_uid, import_ref) is a
// *DuplicateImportRefError.
func (s *Store) insertTx(ctx context.Context, tx *sql.Tx, userUID string, e transaction.Transaction, now string) (transaction.Transaction, error) {
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
	account, toAccount, note, err := s.sealTxText(next)
	if err != nil {
		return e, err
	}
	var id int64
	err = writeChecked(ctx, tx, next, "insert transaction", func() error {
		return tx.QueryRowContext(ctx,
			`INSERT INTO transactions (uid, user_uid, type, amount_minor, amount_scale, currency, balance_uid, account, to_balance_uid, to_account, category_uid, spent_at, spent_at_utc, note, created_at, version, balance_applied,
			                           to_amount_minor, to_amount_scale, to_currency, exchange_rate, rate_source, rate_date, import_ref,
			                           foreign_amount_minor, foreign_amount_scale, foreign_currency, interest_amount_minor, fee_amount_minor,
			                           gain_loss_minor, entire_asset_sale)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, 1, true, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26, $27, $28, $29)
			 RETURNING id`,
			append(append(append(append([]any{next.UID, userUID, string(next.Type), next.AmountMinor, scale, next.Currency, next.BalanceUID, account, next.ToBalanceUID, toAccount, next.CategoryUID, next.SpentAt, spentUTC(next), note, next.CreatedAt},
				fxArgs(next)...), next.ImportRef), foreignArgs(next)...), next.InterestAmountMinor, next.FeeAmountMinor, next.GainLossMinor, next.EntireAssetSale)...).Scan(&id)
	})
	if err != nil {
		return e, err
	}
	next.ID = id
	if err := s.applyBalanceEffects(ctx, tx, userUID, nil, &next, now); err != nil {
		return e, err
	}
	if err := hideSoldAsset(ctx, tx, userUID, next, now); err != nil {
		return e, err
	}
	if err := pullBeginningBalances(ctx, tx, userUID, next, now); err != nil {
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
	// BalanceUID matches transactions that use the balance as source
	// (balance_uid) or destination (to_balance_uid). "" = all.
	BalanceUID common_type.UID
	// After continues a listing: only rows strictly after this position in
	// the list order (spent_at_utc DESC, id DESC). nil = from the start.
	After *ListCursor
	Limit int
}

// ListCursor is a position in the transaction list order (spent_at_utc
// DESC, id DESC): keyset pagination, so pages stay consistent while rows
// are added or deleted.
type ListCursor struct {
	SpentAtUnix int64
	ID          int64
}

// cursorVersion prefixes encoded cursors so the format can change later.
const cursorVersion = "1"

// ErrBadCursor is returned by ParseListCursor for a malformed cursor.
var ErrBadCursor = errors.New("invalid cursor")

// String encodes the cursor as an opaque URL-safe token.
func (c ListCursor) String() string {
	raw := cursorVersion + "." + strconv.FormatInt(c.SpentAtUnix, 10) + "." + strconv.FormatInt(c.ID, 10)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// ParseListCursor decodes a token made by ListCursor.String.
func ParseListCursor(s string) (ListCursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return ListCursor{}, ErrBadCursor
	}
	parts := strings.Split(string(b), ".")
	if len(parts) != 3 || parts[0] != cursorVersion {
		return ListCursor{}, ErrBadCursor
	}
	at, err1 := strconv.ParseInt(parts[1], 10, 64)
	id, err2 := strconv.ParseInt(parts[2], 10, 64)
	if err1 != nil || err2 != nil || id < 1 {
		return ListCursor{}, ErrBadCursor
	}
	return ListCursor{SpentAtUnix: at, ID: id}, nil
}

// cursorOf is the list position of e.
func cursorOf(e transaction.Transaction) ListCursor {
	return ListCursor{SpentAtUnix: e.SpentTime.Unix(), ID: e.ID}
}

const selectCols = `id, uid, type, amount_minor, amount_scale, currency, balance_uid, account, to_balance_uid, to_account, category_uid, spent_at, note, created_at, updated_at, version, balance_applied, adjustment_direction,
	to_amount_minor, to_amount_scale, to_currency, exchange_rate, rate_source, rate_date, import_ref,
	foreign_amount_minor, foreign_amount_scale, foreign_currency, interest_amount_minor, fee_amount_minor,
	gain_loss_minor, entire_asset_sale`

// List returns rows from transactions owned by userUID, newest first (by spend
// time, then ID).
func (s *Store) List(ctx context.Context, userUID string, f ListFilter) ([]transaction.Transaction, error) {
	query := `SELECT ` + selectCols + ` FROM transactions WHERE user_uid = $1`
	args := []any{userUID}
	// arg appends v and returns its placeholder ($2, $3, ...).
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	if f.From != nil {
		query += ` AND spent_at_utc >= ` + arg(f.From.UTC())
	}
	if f.To != nil {
		query += ` AND spent_at_utc <= ` + arg(f.To.UTC())
	}
	if f.CategoryUID != "" {
		query += ` AND category_uid = ` + arg(f.CategoryUID)
	}
	if f.Type != "" {
		query += ` AND type = ` + arg(string(f.Type))
	}
	if f.BalanceUID != "" {
		p := arg(f.BalanceUID)
		query += ` AND (balance_uid = ` + p + ` OR to_balance_uid = ` + p + `)`
	}
	if f.After != nil {
		query += ` AND (spent_at_utc, id) < (` + arg(time.Unix(f.After.SpentAtUnix, 0).UTC()) + `, ` + arg(f.After.ID) + `)`
	}
	query += ` ORDER BY spent_at_utc DESC, id DESC`
	if f.Limit > 0 {
		query += ` LIMIT ` + arg(f.Limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list transactions: %w", err)
	}
	defer rows.Close()

	out := []transaction.Transaction{}
	for rows.Next() {
		e, err := s.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListPage is List for one page of at most f.Limit rows (f.Limit must be >
// 0). next is the cursor for the following page (pass it as f.After), or nil
// when there are no more rows.
func (s *Store) ListPage(ctx context.Context, userUID string, f ListFilter) (items []transaction.Transaction, next *ListCursor, err error) {
	if f.Limit < 1 {
		return nil, nil, fmt.Errorf("list transactions: page limit %d", f.Limit)
	}
	page := f.Limit
	f.Limit++ // one extra row tells whether another page exists
	items, err = s.List(ctx, userUID, f)
	if err != nil {
		return nil, nil, err
	}
	if len(items) > page {
		items = items[:page]
		c := cursorOf(items[page-1])
		next = &c
	}
	return items, next, nil
}

// Get returns one row from transactions owned by userUID or ErrNotFound
// (including when the id belongs to another user).
func (s *Store) Get(ctx context.Context, userUID string, id int64) (transaction.Transaction, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+selectCols+` FROM transactions WHERE id = $1 AND user_uid = $2`, id, userUID)
	e, err := s.scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return transaction.Transaction{}, ErrNotFound
	}
	return e, err
}

// GetByUID returns one row from transactions by its UUID for userUID or
// ErrNotFound (including when the uid belongs to another user).
func (s *Store) GetByUID(ctx context.Context, userUID string, uid common_type.UID) (transaction.Transaction, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+selectCols+` FROM transactions WHERE uid = $1 AND user_uid = $2`, uid, userUID)
	e, err := s.scan(row)
	if errors.Is(err, sql.ErrNoRows) || isInvalidText(err) { // not a UUID: no such transaction
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
	var saved transaction.Transaction
	err := s.inTx(ctx, "update transaction", func(tx *sql.Tx) error {
		// Other writes lock a balance before moving its beginning balance
		// (pullBeginningBalances); an edit of the beginning balance itself
		// takes the same order (its balance never changes), so the two
		// cannot deadlock.
		var typ, buid string
		if err := tx.QueryRowContext(ctx, `SELECT type, balance_uid FROM transactions WHERE id = $1 AND user_uid = $2`, id, userUID).Scan(&typ, &buid); err == nil &&
			typ == string(transaction.BeginningBalance) {
			if _, err := s.lockBalanceUIDs(ctx, tx, userUID, []string{buid}); err != nil {
				return err
			}
		}

		cur, err := s.scan(tx.QueryRowContext(ctx, `SELECT `+selectCols+` FROM transactions WHERE id = $1 AND user_uid = $2 FOR UPDATE`, id, userUID))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if opts.Version > 0 && cur.Version != opts.Version {
			return ErrVersionConflict
		}
		next, err := fn(cur)
		if err != nil {
			return err
		}
		// A server-recorded transaction (balance adjustment, beginning
		// balance) keeps its type, and nothing else becomes one.
		if (cur.Type.ServerRecorded() || next.Type.ServerRecorded()) && cur.Type != next.Type {
			return ErrReadOnly
		}
		if !next.Type.HasDirection() {
			next.AdjustmentDirection = nil
		} else if next.AdjustmentDirection == nil {
			return fmt.Errorf("%s without adjustment_direction", next.Type)
		}
		if next.Type == transaction.BeginningBalance {
			// It stays with its balance and comes before its other transactions.
			if next.BalanceUID != cur.BalanceUID {
				return &validate.ValidationError{Fields: map[string]string{"balance_uid": transaction.ErrBeginningBalanceMove}}
			}
			if err := checkBeginningBalanceDate(ctx, tx, userUID, id, next); err != nil {
				return err
			}
		}
		if err := checkFX(next); err != nil {
			return err
		}
		if err := checkForeign(next); err != nil {
			return err
		}
		if err := checkCharges(next); err != nil {
			return err
		}
		if err := checkSaleResult(next); err != nil {
			return err
		}
		scale, ok := money.MinorUnits(next.Currency)
		if !ok {
			return fmt.Errorf("unknown currency %q", next.Currency)
		}
		now := s.nowRFC3339()
		next.ID, next.UID, next.CreatedAt, next.UpdatedAt = cur.ID, cur.UID, cur.CreatedAt, &now
		next.Version, next.AdjustsBalances = cur.Version+1, cur.AdjustsBalances

		account, toAccount, note, err := s.sealTxText(next)
		if err != nil {
			return err
		}
		var n int64
		err = writeChecked(ctx, tx, next, "update transaction", func() error {
			res, err := tx.ExecContext(ctx,
				`UPDATE transactions SET type = $1, amount_minor = $2, amount_scale = $3, currency = $4, balance_uid = $5, account = $6, to_balance_uid = $7, to_account = $8, category_uid = $9,
			        spent_at = $10, spent_at_utc = $11, note = $12, updated_at = $13, version = $14,
			        to_amount_minor = $15, to_amount_scale = $16, to_currency = $17, exchange_rate = $18, rate_source = $19, rate_date = $20, import_ref = $21,
			        foreign_amount_minor = $22, foreign_amount_scale = $23, foreign_currency = $24,
			        interest_amount_minor = $25, fee_amount_minor = $26, adjustment_direction = $27,
			        gain_loss_minor = $28, entire_asset_sale = $29
			 WHERE id = $30 AND version = $31`,
				append(append(append(append([]any{string(next.Type), next.AmountMinor, scale, next.Currency, next.BalanceUID, account, next.ToBalanceUID, toAccount, next.CategoryUID,
					next.SpentAt, spentUTC(next), note, now, next.Version}, fxArgs(next)...), next.ImportRef), foreignArgs(next)...), next.InterestAmountMinor, next.FeeAmountMinor, directionArg(next),
					next.GainLossMinor, next.EntireAssetSale, id, cur.Version)...)
			if err != nil {
				return err
			}
			n, err = res.RowsAffected()
			return err
		})
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrVersionConflict
		}
		if err := s.applyBalanceEffects(ctx, tx, userUID, &cur, &next, now); err != nil {
			return err
		}
		if err := hideSoldAsset(ctx, tx, userUID, next, now); err != nil {
			return err
		}
		if err := pullBeginningBalances(ctx, tx, userUID, next, now); err != nil {
			return err
		}
		saved = next
		return nil
	})
	if err != nil {
		return transaction.Transaction{}, err
	}
	return saved, nil
}

// DeleteWith removes one row from transactions owned by userUID and reverses
// its balance effect, in one database transaction. Returns ErrNotFound
// (including when the id belongs to another user), ErrVersionConflict when
// opts.Version > 0 and differs from the stored one, or ErrUndeletable for a
// beginning balance.
func (s *Store) DeleteWith(ctx context.Context, userUID string, id int64, opts WriteOptions) error {
	return s.inTx(ctx, "delete transaction", func(tx *sql.Tx) error {

		cur, err := s.scan(tx.QueryRowContext(ctx, `SELECT `+selectCols+` FROM transactions WHERE id = $1 AND user_uid = $2 FOR UPDATE`, id, userUID))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if opts.Version > 0 && cur.Version != opts.Version {
			return ErrVersionConflict
		}
		if cur.Type == transaction.BeginningBalance {
			return ErrUndeletable
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM transactions WHERE id = $1`, id); err != nil {
			return fmt.Errorf("delete transaction: %w", err)
		}
		if err := s.applyBalanceEffects(ctx, tx, userUID, &cur, nil, s.nowRFC3339()); err != nil {
			return err
		}
		return nil
	})
}

// directionArg is the adjustment_direction column value (NULL unless next is
// a balance adjustment or beginning balance).
func directionArg(next transaction.Transaction) any {
	if next.AdjustmentDirection == nil {
		return nil
	}
	return string(*next.AdjustmentDirection)
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

// checkForeign enforces the foreign-amount invariants: the fields are set
// together, only on a type that allows them, in a known currency other than
// the transaction currency, with a positive amount.
func checkForeign(e transaction.Transaction) error {
	if e.ForeignCurrency == nil && e.ForeignAmountMinor == nil && e.ForeignAmount == nil {
		return nil
	}
	if !e.Type.AllowsForeignAmount() || e.ForeignCurrency == nil || e.ForeignAmountMinor == nil || *e.ForeignAmountMinor <= 0 ||
		*e.ForeignCurrency == e.Currency {
		return fmt.Errorf("invalid foreign amount fields on a %s transaction", e.Type)
	}
	if _, ok := money.MinorUnits(*e.ForeignCurrency); !ok {
		return fmt.Errorf("unknown currency %q", *e.ForeignCurrency)
	}
	return nil
}

// foreignArgs are the values of the foreign_amount_minor, foreign_amount_scale
// and foreign_currency columns.
func foreignArgs(e transaction.Transaction) []any {
	if e.ForeignCurrency == nil {
		return []any{nil, nil, nil}
	}
	scale, _ := money.MinorUnits(*e.ForeignCurrency)
	return []any{*e.ForeignAmountMinor, scale, *e.ForeignCurrency}
}

// checkCharges enforces the debt-charge invariants: interest and fees are
// positive when set, only on a repay_debt, and a repay_debt has a category
// only when it has charges.
func checkCharges(e transaction.Transaction) error {
	for _, m := range []*int64{e.InterestAmountMinor, e.FeeAmountMinor} {
		if m != nil && (*m <= 0 || !e.Type.AllowsCharges()) {
			return fmt.Errorf("invalid interest/fee fields on a %s transaction", e.Type)
		}
	}
	if e.Type == transaction.RepayDebt && e.CategoryUID != nil && e.ChargesMinor() == 0 {
		return errors.New("repay_debt category without interest or fees")
	}
	return nil
}

// checkSaleResult enforces the asset-sale invariants: gain_loss and
// entire_asset_sale only on an asset_sale, gain_loss non-zero when set and
// never a loss larger than the principal, and a category on an asset_sale
// only when it has a gain or loss.
func checkSaleResult(e transaction.Transaction) error {
	if !e.Type.AllowsSaleResult() {
		if e.GainLossMinor != nil || e.EntireAssetSale {
			return fmt.Errorf("invalid asset sale fields on a %s transaction", e.Type)
		}
		return nil
	}
	if g := e.GainLossMinor; g != nil && (*g == 0 || e.AmountMinor+*g < 0) {
		return errors.New("invalid gain_loss on an asset_sale")
	}
	if e.CategoryUID != nil && e.GainLossValue() == 0 {
		return errors.New("asset_sale category without a gain or loss")
	}
	return nil
}

// hideSoldAsset hides the asset sold by e (to_balance_uid) when e is an
// entire asset sale and the balance is not hidden yet (version +1). The
// balance is never shown again automatically.
func hideSoldAsset(ctx context.Context, tx *sql.Tx, userUID string, e transaction.Transaction, now string) error {
	if e.Type != transaction.AssetSale || !e.EntireAssetSale || e.ToBalanceUID == nil {
		return nil
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE balances SET hidden = true, updated_at = $1, version = version + 1 WHERE user_uid = $2 AND uid = $3 AND NOT hidden`,
		now, userUID, *e.ToBalanceUID); err != nil {
		return fmt.Errorf("hide sold asset: %w", err)
	}
	return nil
}

// spentUTC is the spent_at_utc value of e: its spend time at second
// precision (spent_at is stored at second precision too), in UTC.
func spentUTC(e transaction.Transaction) time.Time {
	return e.SpentTime.Truncate(time.Second).UTC()
}

func (s *Store) scan(r scanner) (transaction.Transaction, error) {
	var e transaction.Transaction
	var scale int
	var created time.Time
	var updated, rateDate sql.NullTime
	var toUID, toName, catUID, dir sql.NullString
	var typ string
	var toMinor, toScale sql.NullInt64
	var toCur, rate, rateSrc, importRef sql.NullString
	var fMinor, fScale sql.NullInt64
	var fCur sql.NullString
	var interest, fee, gainLoss sql.NullInt64
	if err := r.Scan(&e.ID, &e.UID, &typ, &e.AmountMinor, &scale, &e.Currency, &e.BalanceUID, &e.Account, &toUID, &toName, &catUID, &e.SpentAt, &e.Note, &created, &updated,
		&e.Version, &e.AdjustsBalances, &dir, &toMinor, &toScale, &toCur, &rate, &rateSrc, &rateDate, &importRef,
		&fMinor, &fScale, &fCur, &interest, &fee, &gainLoss, &e.EntireAssetSale); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return e, err
		}
		return e, fmt.Errorf("scan transaction: %w", err)
	}
	if toName.Valid {
		e.ToAccount = &toName.String
	}
	if err := s.openTxText(&e); err != nil {
		return e, fmt.Errorf("scan transaction %d: %w", e.ID, err)
	}
	e.Amount = money.FormatAmount(e.AmountMinor, scale)
	e.CreatedAt, e.UpdatedAt = ts(created), nullTS(updated)
	e.Type = transaction.Type(typ)
	if toUID.Valid {
		to := transaction.UID(toUID.String)
		e.ToBalanceUID = &to
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
			d := dateOnly(rateDate.Time)
			e.RateDate = &d
		}
	}
	if importRef.Valid {
		e.ImportRef = &importRef.String
	}
	if fMinor.Valid && fCur.Valid {
		m, a, c := fMinor.Int64, money.FormatAmount(fMinor.Int64, int(fScale.Int64)), fCur.String
		e.ForeignAmountMinor, e.ForeignAmount, e.ForeignCurrency = &m, &a, &c
	}
	// Interest and fees are in the transaction currency, at amount_scale.
	if interest.Valid {
		m, a := interest.Int64, money.FormatAmount(interest.Int64, scale)
		e.InterestAmountMinor, e.InterestAmount = &m, &a
	}
	if fee.Valid {
		m, a := fee.Int64, money.FormatAmount(fee.Int64, scale)
		e.FeeAmountMinor, e.FeeAmount = &m, &a
	}
	if gainLoss.Valid {
		m, a := gainLoss.Int64, money.FormatAmount(gainLoss.Int64, scale)
		e.GainLossMinor, e.GainLoss = &m, &a
	}
	if t, err := time.Parse(time.RFC3339, e.SpentAt); err == nil {
		e.SpentTime = t
	}
	return e, nil
}

// sealTxText returns the stored (encrypted) account, to_account and note of
// e; to_account is nil for NULL.
func (s *Store) sealTxText(e transaction.Transaction) (account string, toAccount any, note string, err error) {
	if account, err = s.seal(e.Account); err != nil {
		return
	}
	if toAccount, err = s.sealPtr(e.ToAccount); err != nil {
		return
	}
	note, err = s.seal(e.Note)
	return
}

// openTxText decrypts e's account, to_account and note in place.
func (s *Store) openTxText(e *transaction.Transaction) error {
	if err := s.open(&e.Account, "account"); err != nil {
		return err
	}
	if err := s.open(e.ToAccount, "to_account"); err != nil {
		return err
	}
	return s.open(&e.Note, "note")
}
