// Package store persists the app's data (transactions, balances, users,
// categories, sessions, API tokens and the exchange-rate cache) in Postgres,
// through database/sql and the pgx driver (pure Go, no CGO). This file holds
// the Store itself and the errors shared by every table; each table's
// queries live in their own file (transactions.go, balances.go, users.go,
// ...). The schema is in schema.go; versioning and migrations live in
// migrate.go.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/nutmos/expensetrackr/pkg/helper"
)

// ErrNotFound is returned when the requested row does not exist (or belongs
// to another user). It is shared by every table, so its message names none;
// the API answers "<resource> not found" for the route (issue #65).
var ErrNotFound = errors.New("not found")

// ErrVersionConflict is returned by an update or delete whose expected
// version (WriteOptions.Version) no longer matches the stored row: someone
// else changed it since the caller read it. Nothing is written.
var ErrVersionConflict = errors.New("version conflict: the record was changed since it was read")

// ErrReadOnly is returned when a write tries to create a server-recorded
// transaction (balance_adjustment, beginning_balance), or to change the type
// of a transaction to or from one. Balance adjustments are created
// automatically by manual balance edits (UpdateBalanceWith) and beginning
// balances when a balance is created (CreateBalance); once recorded they can
// be edited (keeping their type), and balance adjustments deleted, like any
// other transaction.
var ErrReadOnly = errors.New("balance adjustments and beginning balances are created automatically (by balance edits and when a balance is created); they cannot be created directly, and a transaction cannot change type to or from balance_adjustment or beginning_balance")

// ErrUndeletable is returned when a write tries to delete a beginning_balance
// transaction: it goes only together with its balance (DeleteBalanceWith).
var ErrUndeletable = errors.New("a beginning balance cannot be deleted; edit it instead (it is removed only together with its balance)")

// WriteOptions controls optimistic locking for updates and deletes.
type WriteOptions struct {
	// Version, if > 0, must equal the stored version or the write fails with
	// ErrVersionConflict. 0 skips the check (internal callers and tests; the
	// API always sends the client's version for PUT).
	Version int64
}

// Store wraps the Postgres connection pool.
type Store struct {
	db  *sql.DB
	now func() time.Time // clock for created_at/updated_at; tests may override
}

// Connection pool limits. The app is small; a handful of connections serve
// concurrent requests while writes that touch the same rows serialize on
// row locks (SELECT ... FOR UPDATE).
const (
	maxOpenConns    = 10
	maxIdleConns    = 5
	connMaxIdleTime = 5 * time.Minute
	openTimeout     = 10 * time.Second
)

// nowRFC3339 is the current time from s.now, in UTC, formatted for storage.
func (s *Store) nowRFC3339() string { return ts(s.now()) }

// Open connects to the Postgres database at url (a postgres:// URL or a
// key=value connection string), checks it is reachable and brings its schema
// up to date (see migrate). A database from a newer build fails with
// ErrUnsupportedSchema and is not modified. A search_path in url selects the
// schema (tests use one schema each).
func Open(url string) (*Store, error) {
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("database url: %w", err)
	}
	db := stdlib.OpenDB(*cfg)
	db.SetMaxOpenConns(maxOpenConns)
	db.SetMaxIdleConns(maxIdleConns)
	db.SetConnMaxIdleTime(connMaxIdleTime)
	ctx, cancel := context.WithTimeout(context.Background(), openTimeout)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect to %s: %w", describe(cfg), err)
	}
	if err := migrate(ctx, db); err != nil {
		db.Close()
		return nil, fmt.Errorf("database %s: %w", describe(cfg), err)
	}
	if err := checkNotEncrypted(ctx, db); err != nil {
		db.Close()
		return nil, fmt.Errorf("database %s: %w", describe(cfg), err)
	}
	return &Store{db: db, now: time.Now}, nil
}

// describe names a database for error messages without its password.
func describe(cfg *pgx.ConnConfig) string {
	return fmt.Sprintf("%s@%s:%d/%s", cfg.User, cfg.Host, cfg.Port, cfg.Database)
}

// CreateTestSchema creates an empty, uniquely named schema in the database
// at url and returns a URL whose connections use it (search_path), plus a
// function that drops it with everything in it. Tests use it to get a
// private, throwaway database; see pkg/store/storetest.
func CreateTestSchema(url string) (schemaURL string, drop func() error, err error) {
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		return "", nil, fmt.Errorf("test database url: %w", err)
	}
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	schema := "test_" + hex.EncodeToString(b)
	ctx, cancel := context.WithTimeout(context.Background(), openTimeout)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return "", nil, fmt.Errorf("connect to test database %s: %w", describe(cfg), err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		return "", nil, fmt.Errorf("create test schema: %w", err)
	}
	drop = func() error {
		ctx, cancel := context.WithTimeout(context.Background(), openTimeout)
		defer cancel()
		c, err := pgx.ConnectConfig(ctx, cfg)
		if err != nil {
			return err
		}
		defer c.Close(ctx)
		_, err = c.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		return err
	}
	return helper.WithSearchPath(url, schema), drop, nil
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
