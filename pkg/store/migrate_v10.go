package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Migration 9 -> 10: encryption at rest (issue #192).
//
// Encryption at rest has since been removed. This step stays so databases
// already at v10 still open and new ones reach the same schema; the table
// stays empty and Open refuses a database where it is not
// (checkNotEncrypted). What it did:
//
//   - encryption_state: at most one row (id = 1), present once the database
//     is encrypted: the id of the key that encrypted it and a known value
//     sealed with that key (to recognise a wrong key at startup).
//   - user_categories.categories may also be a jsonb string: the sealed JSON
//     text of the array, when encryption is on.
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

// ErrEncryptedDatabase is returned by Open for a database that was encrypted
// at rest (by a build that had the feature, with a key). This build stores
// and reads plain text only, so it would show ciphertext; it refuses instead.
var ErrEncryptedDatabase = errors.New("the database was encrypted at rest (EXPENSE_ENCRYPTION_KEY), which this version no longer supports; restore a backup taken before encryption or decrypt it with the earlier build")

// checkNotEncrypted fails with ErrEncryptedDatabase when encryption_state has
// a row, i.e. the stored text is ciphertext.
func checkNotEncrypted(ctx context.Context, db *sql.DB) error {
	var encrypted bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM encryption_state)`).Scan(&encrypted); err != nil {
		return fmt.Errorf("read encryption_state: %w", err)
	}
	if encrypted {
		return ErrEncryptedDatabase
	}
	return nil
}
