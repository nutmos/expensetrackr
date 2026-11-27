package store

import (
	"context"
	"database/sql"
	"fmt"
)

// Migration 9 -> 10: encryption at rest (issue #192, encryption.go).
//
//   - encryption_state: at most one row (id = 1), present once the database
//     is encrypted: the id of the key that encrypted it and a known value
//     sealed with that key (to recognise a wrong key at startup).
//   - user_categories.categories may also be a jsonb string: the sealed JSON
//     text of the array, when encryption is on.
//
// Nothing is encrypted here: the store does that at Open when it has a key.
func addEncryptionState(ctx context.Context, tx *sql.Tx) error {
	var catCheck string
	if err := tx.QueryRowContext(ctx, `SELECT conname FROM pg_constraint
		WHERE conrelid = 'user_categories'::regclass AND contype = 'c'
		  AND pg_get_constraintdef(oid) LIKE '%jsonb_typeof(categories)%'`).Scan(&catCheck); err != nil {
		return fmt.Errorf("find categories check: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE user_categories
		DROP CONSTRAINT `+quoteIdent(catCheck)+`,
		ADD CONSTRAINT user_categories_categories_check CHECK (jsonb_typeof(categories) IN ('array', 'string'))`); err != nil {
		return fmt.Errorf("allow encrypted categories: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE encryption_state (
		id          smallint    PRIMARY KEY CHECK (id = 1),
		key_id      text        NOT NULL CHECK (char_length(key_id) = 8),
		check_value text        NOT NULL,
		updated_at  timestamptz NOT NULL
	)`); err != nil {
		return fmt.Errorf("create encryption_state: %w", err)
	}
	return nil
}
