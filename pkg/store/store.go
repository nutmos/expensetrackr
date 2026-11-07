// Package store persists the app's data (transactions, balances, users,
// categories, sessions, API tokens and the exchange-rate cache) in SQLite,
// using the pure-Go modernc.org/sqlite driver (no CGO required). This file
// holds the Store itself and the errors shared by every table; each table's
// queries live in their own file (transactions.go, balances.go, users.go,
// ...). The schema is in schema.go; versioning and migrations live in
// migrate.go.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// ErrNotFound is returned when the requested row does not exist (or belongs
// to another user). It is shared by every table, so its message names none;
// the API answers "<resource> not found" for the route (issue #65).
var ErrNotFound = errors.New("not found")

// ErrVersionConflict is returned by an update or delete whose expected
// version (WriteOptions.Version) no longer matches the stored row: someone
// else changed it since the caller read it. Nothing is written.
var ErrVersionConflict = errors.New("version conflict: the record was changed since it was read")

// ErrReadOnly is returned when an update or delete targets a
// balance_adjustment transaction, or a write tries to give a transaction that
// type. Balance adjustments are created automatically by manual balance edits
// (UpdateBalanceWith) and are never changed afterwards.
var ErrReadOnly = errors.New("balance adjustments are created automatically and cannot be created, edited or deleted directly")

// WriteOptions controls optimistic locking for updates and deletes.
type WriteOptions struct {
	// Version, if > 0, must equal the stored version or the write fails with
	// ErrVersionConflict. 0 skips the check (internal callers and tests; the
	// API always sends the client's version for PUT).
	Version int64
}

// Store wraps the SQLite database.
type Store struct {
	db  *sql.DB
	now func() time.Time // clock for created_at/updated_at; tests may override
}

// busyTimeoutMS is how long SQLite waits on a locked database before failing.
const busyTimeoutMS = 5000

// nowRFC3339 is the current time from s.now, in UTC, formatted for storage.
func (s *Store) nowRFC3339() string { return ts(s.now()) }

// Open opens (creating if needed) the SQLite database at path and brings its
// schema up to date (see migrate). A database created before the schema
// baseline reset, or by a newer build, fails with ErrUnsupportedSchema and is
// not modified.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db directory: %w", err)
		}
	}
	q := url.Values{}
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busyTimeoutMS))
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(1)")
	dsn := "file:" + path + "?" + q.Encode()

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// A single connection keeps SQLite writes simple and safe for a small service.
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("database %s: %w", path, err)
	}
	return &Store{db: db, now: time.Now}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// errRollback, returned by an inTx callback, rolls the transaction back
// without failing: inTx then returns nil (dry runs, failed atomic batches).
var errRollback = errors.New("rollback requested")

// inTx runs fn in one database transaction: it commits if fn returns nil and
// rolls back otherwise. fn's error is returned unchanged (sentinels such as
// ErrNotFound stay comparable with errors.Is and keep their text); begin and
// commit failures are wrapped as "op: ...". A panic in fn rolls back too.
func (s *Store) inTx(ctx context.Context, op string, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit
	if err := fn(tx); err != nil {
		if errors.Is(err, errRollback) {
			return nil
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	return nil
}

// scanner is a *sql.Row or *sql.Rows; the scan helpers of every table take it.
type scanner interface{ Scan(dest ...any) error }
