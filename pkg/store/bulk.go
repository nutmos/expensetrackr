package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

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
	err := q.QueryRowContext(ctx, `SELECT uid FROM transactions WHERE balance_uid = $1 AND import_ref = $2`, balanceUID, importRef).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("look up import_ref: %w", err)
	}
	return uid, nil
}

// writeChecked runs write, an INSERT or UPDATE of transaction e inside tx,
// and turns a violation of the unique (balance_uid, import_ref) index into a
// *DuplicateImportRefError naming the existing transaction. Other errors are
// wrapped as "op: ...".
//
// In Postgres a failed statement aborts the whole transaction, so when e has
// an import_ref the write runs inside a savepoint: on a violation the
// transaction is rolled back to it and stays usable for the lookup (and for
// the caller, e.g. the next item of a bulk import).
func writeChecked(ctx context.Context, tx *sql.Tx, e transaction.Transaction, op string, write func() error) error {
	if e.ImportRef == nil {
		if err := write(); err != nil {
			return fmt.Errorf("%s: %w", op, err)
		}
		return nil
	}
	if _, err := tx.ExecContext(ctx, `SAVEPOINT import_ref`); err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	werr := write()
	if werr != nil {
		if _, err := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT import_ref`); err != nil {
			return fmt.Errorf("%s: %w", op, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `RELEASE SAVEPOINT import_ref`); err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	if werr == nil {
		return nil
	}
	if isUniqueViolation(werr, "transactions_import_ref_uq") {
		uid, lerr := existingImportRef(ctx, tx, string(e.BalanceUID), *e.ImportRef)
		if lerr == nil && uid != "" {
			return &DuplicateImportRefError{ImportRef: *e.ImportRef, ExistingUID: uid}
		}
	}
	return fmt.Errorf("%s: %w", op, werr)
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
func (s *Store) CreateBulk(ctx context.Context, userUID string, items []BulkItem, opts BulkOptions) (BulkOutcome, error) {
	out := BulkOutcome{Results: make([]BulkResult, len(items))}
	rolledBack := false
	err := s.inTx(ctx, "bulk insert", func(tx *sql.Tx) error {
		now := s.nowRFC3339()
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
					return err
				}
				if uid != "" {
					r.Status, r.ExistingUID = BulkSkipped, uid
					continue
				}
			}
			if _, err := tx.ExecContext(ctx, `SAVEPOINT bulk_item`); err != nil {
				return fmt.Errorf("bulk insert: %w", err)
			}
			next, err := insertTx(ctx, tx, userUID, e, now)
			if err != nil {
				for _, q := range []string{`ROLLBACK TO bulk_item`, `RELEASE bulk_item`} {
					if _, rerr := tx.ExecContext(ctx, q); rerr != nil {
						return fmt.Errorf("bulk insert: %w", rerr)
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
				return fmt.Errorf("bulk insert: %w", err)
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
			b, err := balanceByUIDTx(ctx, tx, userUID, uid)
			if errors.Is(err, ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			out.Balances = append(out.Balances, b)
		}
		if opts.DryRun || (opts.Atomic && failed > 0) {
			rolledBack = true
			return errRollback
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	out.Committed = !rolledBack
	return out, nil
}
