package store

import (
	"database/sql"
	"fmt"
)

// Migration 4 -> 5: drop redundant uid indexes (issue #90).
//
// transactions.uid and balances.uid are declared UNIQUE, so SQLite already
// keeps an index for each (sqlite_autoindex_<table>_1). The explicit unique
// indexes idx_transactions_uid and idx_balances_uid duplicated them and only
// added write cost. Uniqueness and uid lookups are unchanged.
func dropRedundantUIDIndexes(tx *sql.Tx) error {
	for _, name := range []string{"idx_transactions_uid", "idx_balances_uid"} {
		if _, err := tx.Exec(`DROP INDEX IF EXISTS ` + name); err != nil {
			return fmt.Errorf("drop index %s: %w", name, err)
		}
	}
	return nil
}
