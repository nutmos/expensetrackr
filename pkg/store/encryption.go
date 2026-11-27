package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/nutmos/expensetrackr/pkg/fieldcrypt"
)

// Encryption at rest (issue #192, docs/encryption.md).
//
// With a keyring (Open with WithKeyring), the free-text personal data is
// stored encrypted with AES-256-GCM (pkg/fieldcrypt):
//
//	transactions.note, transactions.account, transactions.to_account
//	balances.name, balances.description
//	users.display_name
//	user_categories.categories (the whole JSON array, stored as a JSON string)
//
// Amounts, currencies, dates, types and ids stay in clear: the database sums,
// filters and orders by them. Usernames and emails stay in clear too: login
// looks users up by them and they must be unique.
//
// The table encryption_state (migration v10) remembers that the database is
// encrypted and with which key: one row holding the primary key's id and a
// known value sealed with it. Open uses it to refuse to start without the key
// (or with the wrong one) instead of showing ciphertext, and to notice a new
// primary key, after which it re-encrypts every value with it (rotation).
// The first Open with a key encrypts the existing plain-text rows.

// Option configures Open.
type Option func(*Store)

// WithKeyring makes the store encrypt personal data with k (nil: no
// encryption, which is the default).
func WithKeyring(k *fieldcrypt.Keyring) Option {
	return func(s *Store) { s.keys = k }
}

var (
	// ErrEncryptionKeyRequired: the database is encrypted but Open got no key.
	ErrEncryptionKeyRequired = errors.New("the database is encrypted: set EXPENSE_ENCRYPTION_KEY to its key")
	// ErrWrongEncryptionKey: the database is encrypted with a key Open does
	// not have.
	ErrWrongEncryptionKey = errors.New("the database is encrypted with a different key: set EXPENSE_ENCRYPTION_KEY to it, or keep it in EXPENSE_ENCRYPTION_OLD_KEYS while rotating")
)

// checkPlain is the value sealed into encryption_state.check_value.
const checkPlain = "expensetrackr encryption check"

// encryptedColumns are the plain text columns that hold sealed values, as
// (table, column). user_categories.categories (jsonb) is handled apart.
var encryptedColumns = [][2]string{
	{"transactions", "note"},
	{"transactions", "account"},
	{"transactions", "to_account"},
	{"balances", "name"},
	{"balances", "description"},
	{"users", "display_name"},
}

// seal encrypts v for storage (unchanged when encryption is off or v is "").
func (s *Store) seal(v string) (string, error) {
	out, err := s.keys.Seal(v)
	if err != nil {
		return "", fmt.Errorf("encrypt: %w", err)
	}
	return out, nil
}

// sealPtr is seal for a nullable column: nil stays NULL.
func (s *Store) sealPtr(v *string) (any, error) {
	if v == nil {
		return nil, nil
	}
	return s.seal(*v)
}

// open decrypts a stored value in place; what names the column for errors.
func (s *Store) open(v *string, what string) error {
	if v == nil {
		return nil
	}
	out, err := s.keys.Open(*v)
	if err != nil {
		return fmt.Errorf("decrypt %s: %w", what, err)
	}
	*v = out
	return nil
}

// prepareEncryption runs at Open, after the migrations: it checks the key
// against encryption_state and encrypts (first key) or re-encrypts (new
// primary key) the stored values, all in one SQL transaction.
func (s *Store) prepareEncryption(ctx context.Context) error {
	return s.inTx(ctx, "encryption", func(tx *sql.Tx) error {
		// One server at a time, like migrate.
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('expensetrackr.encrypt.' || current_schema()))`); err != nil {
			return fmt.Errorf("encryption: lock: %w", err)
		}
		var keyID, check string
		err := tx.QueryRowContext(ctx, `SELECT key_id, check_value FROM encryption_state WHERE id = 1`).Scan(&keyID, &check)
		encrypted := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("encryption: read state: %w", err)
		}
		switch {
		case !s.keys.Enabled() && !encrypted:
			return nil // encryption off, as it always was
		case !s.keys.Enabled():
			return ErrEncryptionKeyRequired
		case encrypted:
			if !s.keys.Has(keyID) {
				return fmt.Errorf("%w (the database's key id is %s, EXPENSE_ENCRYPTION_KEY's is %s)", ErrWrongEncryptionKey, keyID, s.keys.PrimaryID())
			}
			if got, err := s.keys.Open(check); err != nil || got != checkPlain {
				return fmt.Errorf("%w (its check value does not decrypt)", ErrWrongEncryptionKey)
			}
			if keyID == s.keys.PrimaryID() {
				return nil // up to date
			}
		}
		start := time.Now()
		n, err := s.sealAll(ctx, tx, encrypted)
		if err != nil {
			return fmt.Errorf("encryption: %w", err)
		}
		newCheck, err := s.seal(checkPlain)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO encryption_state (id, key_id, check_value, updated_at) VALUES (1, $1, $2, $3)
			ON CONFLICT (id) DO UPDATE SET key_id = EXCLUDED.key_id, check_value = EXCLUDED.check_value, updated_at = EXCLUDED.updated_at`,
			s.keys.PrimaryID(), newCheck, time.Now().UTC()); err != nil {
			return fmt.Errorf("encryption: write state: %w", err)
		}
		if encrypted {
			log.Printf("store: re-encrypted %d value(s) with key %s (was %s) in %s", n, s.keys.PrimaryID(), keyID, time.Since(start).Round(time.Millisecond))
		} else {
			log.Printf("store: encrypted %d existing value(s) with key %s in %s", n, s.keys.PrimaryID(), time.Since(start).Round(time.Millisecond))
		}
		return nil
	})
}

// reseal returns v sealed with the primary key and whether it changed. In a
// database that was not encrypted yet (wasEncrypted false) every value is
// plain text, even one that happens to look sealed; otherwise a value sealed
// with an older key is opened and sealed again, and one already sealed with
// the primary key is kept.
func (s *Store) reseal(v string, wasEncrypted bool) (string, bool, error) {
	if v == "" {
		return v, false, nil
	}
	plain := v
	if wasEncrypted {
		switch id := fieldcrypt.KeyID(v); {
		case id == s.keys.PrimaryID():
			return v, false, nil
		case id != "":
			var err error
			if plain, err = s.keys.Open(v); err != nil {
				return "", false, err
			}
		}
	}
	out, err := s.seal(plain)
	return out, true, err
}

// sealAll seals every value of the encrypted columns that is not sealed with
// the primary key yet, and returns how many it changed.
func (s *Store) sealAll(ctx context.Context, tx *sql.Tx, wasEncrypted bool) (int, error) {
	total := 0
	for _, tc := range encryptedColumns {
		table, col := tc[0], tc[1]
		n, err := s.sealColumn(ctx, tx, table, col, wasEncrypted)
		if err != nil {
			return total, fmt.Errorf("%s.%s: %w", table, col, err)
		}
		total += n
	}
	n, err := s.sealCategories(ctx, tx, wasEncrypted)
	if err != nil {
		return total, fmt.Errorf("user_categories.categories: %w", err)
	}
	return total + n, nil
}

func (s *Store) sealColumn(ctx context.Context, tx *sql.Tx, table, col string, wasEncrypted bool) (int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, `+col+` FROM `+table+` WHERE `+col+` <> '' ORDER BY id`)
	if err != nil {
		return 0, err
	}
	type change struct {
		id  int64
		val string
	}
	var changes []change
	for rows.Next() {
		var c change
		if err := rows.Scan(&c.id, &c.val); err != nil {
			rows.Close()
			return 0, err
		}
		changes = append(changes, c)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n := 0
	for _, c := range changes {
		out, changed, err := s.reseal(c.val, wasEncrypted)
		if err != nil {
			return n, fmt.Errorf("row %d: %w", c.id, err)
		}
		if !changed {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE `+table+` SET `+col+` = $1 WHERE id = $2`, out, c.id); err != nil {
			return n, fmt.Errorf("row %d: %w", c.id, err)
		}
		n++
	}
	return n, nil
}

// sealCategories seals each user's category array: a plain jsonb array
// becomes a jsonb string holding the sealed JSON text.
func (s *Store) sealCategories(ctx context.Context, tx *sql.Tx, wasEncrypted bool) (int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT user_uid, jsonb_typeof(categories), CASE jsonb_typeof(categories) WHEN 'string' THEN categories #>> '{}' ELSE categories::text END
		FROM user_categories ORDER BY user_uid`)
	if err != nil {
		return 0, err
	}
	type change struct{ uid, typ, val string }
	var changes []change
	for rows.Next() {
		var c change
		if err := rows.Scan(&c.uid, &c.typ, &c.val); err != nil {
			rows.Close()
			return 0, err
		}
		changes = append(changes, c)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n := 0
	for _, c := range changes {
		var out string
		if c.typ == "string" {
			var changed bool
			if out, changed, err = s.reseal(c.val, true); err != nil {
				return n, fmt.Errorf("user %s: %w", c.uid, err)
			}
			if !changed {
				continue
			}
		} else if out, err = s.seal(c.val); err != nil {
			return n, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE user_categories SET categories = to_jsonb($1::text) WHERE user_uid = $2`, out, c.uid); err != nil {
			return n, fmt.Errorf("user %s: %w", c.uid, err)
		}
		n++
	}
	return n, nil
}
