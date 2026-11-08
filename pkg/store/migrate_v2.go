package store

import (
	"context"
	"database/sql"
	"fmt"
)

// Migration 1 -> 2: balance names no longer have to be unique.
//
// The v1 baseline had a unique index on balances (user_uid, lower(name)), so a
// user could not have two balances with the same name (case-insensitively).
// Names are labels only: transactions refer to balances by uid, and the web
// page's dropdowns also show each balance's type and amount. The index is
// dropped; balances_user_type_idx (user_uid, type, name) still serves the
// sorted list. Category names stay unique per user and type (checked in Go,
// categories.go).
func allowDuplicateBalanceNames(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `DROP INDEX IF EXISTS balances_user_name_uq`); err != nil {
		return fmt.Errorf("drop balances_user_name_uq: %w", err)
	}
	return nil
}
