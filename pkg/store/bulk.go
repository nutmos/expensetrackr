package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nutmos/expensetrackr/pkg/balance"
	"github.com/nutmos/expensetrackr/pkg/transaction"
)

// DuplicateImportRefError reports that a transaction with the same
// (balance_uid, import_ref) already exists.
type DuplicateImportRefError struct {
	ImportRef   string
	ExistingUID string
}

func (e *DuplicateImportRefError) Error() string {
	return fmt.Sprintf("import_ref %q is already used by transaction %s on this balance", e.ImportRef, e.ExistingUID)
}

// ExistingImportRef returns the uid of the transaction on balanceUID with
// importRef, or "" if none.
func existingImportRef(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, balanceUID, importRef string) (string, error) {
	var uid string
	err := q.QueryRowContext(ctx, `SELECT uid FROM transactions WHERE balance_uid = ? AND import_ref = ?`, balanceUID, importRef).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("look up import_ref: %w", err)
	}
	return uid, nil
}

// mapImportRefErr turns a unique-index violation on import_ref into a
// *DuplicateImportRefError; other errors are returned unchanged.
func mapImportRefErr(ctx context.Context, tx *sql.Tx, e transaction.Transaction, err error) error {
	if e.ImportRef == nil || !strings.Contains(err.Error(), "transactions.import_ref") {
		return err
	}
	uid, lerr := existingImportRef(ctx, tx, string(e.BalanceUID), *e.ImportRef)
	if lerr != nil || uid == "" {
		return err
	}
	return &DuplicateImportRefError{ImportRef: *e.ImportRef, ExistingUID: uid}
}

// BulkOptions controls CreateBulk.
type BulkOptions struct {
	// Atomic: if any item fails, nothing is written (all items still get a
	// result so the caller sees every error).
	Atomic bool
	// DryRun: run every insert and balance effect, then roll back.
	DryRun bool
}

// BulkItem is one entry for CreateBulk. Items with Err set (failed earlier
// validation) are reported back unchanged and not inserted.
type BulkItem struct {
	Tx  transaction.Transaction
	Err error
}

// BulkStatus is the outcome of one item.
type BulkStatus string

const (
	BulkCreated BulkStatus = "created"
	BulkValid   BulkStatus = "valid" // dry run: would be created
	BulkSkipped BulkStatus = "skipped"
	BulkFailed  BulkStatus = "failed"
)

// BulkResult is the outcome of one BulkItem, in input order.
type BulkResult struct {
	Status      BulkStatus
	Tx          transaction.Transaction // created / valid
	ExistingUID string                  // skipped
	Err         error                   // failed
}

// BulkOutcome is what CreateBulk did.
type BulkOutcome struct {
	Results []BulkResult
	// Committed is true when the writes were kept (not a dry run and not an
	// atomic batch with failures).
	Committed bool
	// Balances are the balances touched by created/valid items, as they are
	// (or, for a dry run / rolled-back batch, would be) after the batch, in
	// first-touched order.
	Balances []balance.Balance
}

// CreateBulk inserts items in input order in ONE database transaction, each
// item inside a savepoint so a failing item (e.g. currency mismatch) is
// undone on its own. An item whose (balance_uid, import_ref) already exists,
// in the database or earlier in the same batch, is skipped with the existing
// uid. With opts.Atomic any failure rolls back everything; with opts.DryRun
// everything is always rolled back. No network calls happen here: FX rates
// must already be resolved on the items.
func (s *Store) CreateBulk(ctx context.Context, items []BulkItem, opts BulkOptions) (BulkOutcome, error) {
	out := BulkOutcome{Results: make([]BulkResult, len(items))}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, fmt.Errorf("bulk insert: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit

	now := time.Now().UTC().Format(time.RFC3339)
	failed := 0
	var touched []string
	seen := map[string]bool{}
	touch := func(uid string) {
		if !seen[uid] {
			seen[uid] = true
			touched = append(touched, uid)
		}
	}
	for i, it := range items {
		r := &out.Results[i]
		if it.Err != nil {
			r.Status, r.Err = BulkFailed, it.Err
			failed++
			continue
		}
		e := it.Tx
		if e.Type == transaction.BalanceAdjustment {
			r.Status, r.Err = BulkFailed, ErrReadOnly
			failed++
			continue
		}
		if err := checkFX(e); err != nil {
			r.Status, r.Err = BulkFailed, err
			failed++
			continue
		}
		if e.ImportRef != nil {
			uid, err := existingImportRef(ctx, tx, string(e.BalanceUID), *e.ImportRef)
			if err != nil {
				return out, err
			}
			if uid != "" {
				r.Status, r.ExistingUID = BulkSkipped, uid
				continue
			}
		}
		if _, err := tx.ExecContext(ctx, `SAVEPOINT bulk_item`); err != nil {
			return out, fmt.Errorf("bulk insert: %w", err)
		}
		next, err := insertTx(ctx, tx, e, now)
		if err != nil {
			for _, q := range []string{`ROLLBACK TO bulk_item`, `RELEASE bulk_item`} {
				if _, rerr := tx.ExecContext(ctx, q); rerr != nil {
					return out, fmt.Errorf("bulk insert: %w", rerr)
				}
			}
			var dup *DuplicateImportRefError
			if errors.As(err, &dup) {
				r.Status, r.ExistingUID = BulkSkipped, dup.ExistingUID
				continue
			}
			r.Status, r.Err = BulkFailed, err
			failed++
			continue
		}
		if _, err := tx.ExecContext(ctx, `RELEASE bulk_item`); err != nil {
			return out, fmt.Errorf("bulk insert: %w", err)
		}
		r.Status, r.Tx = BulkCreated, next
		if opts.DryRun {
			r.Status = BulkValid
		}
		for _, ef := range next.Effects() {
			touch(string(ef.BalanceUID))
		}
	}
	for _, uid := range touched {
		b, err := balanceByUIDTx(ctx, tx, uid)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return out, err
		}
		out.Balances = append(out.Balances, b)
	}
	if opts.DryRun || (opts.Atomic && failed > 0) {
		return out, nil // deferred rollback
	}
	if err := tx.Commit(); err != nil {
		return out, fmt.Errorf("bulk insert: %w", err)
	}
	out.Committed = true
	return out, nil
}
