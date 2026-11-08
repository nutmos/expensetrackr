package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
)

// Schema versioning.
//
// Applied versions are recorded in the table schema_migrations (one row per
// version). migrations[i] upgrades a database from version i to i+1;
// migrations[0] creates the baseline (schema.go) in an empty database.
//
//	v1  baseline: the complete schema in baselineSchema (schema.go)
//	v2  balance names no longer unique (migrate_v2.go)
//
// All pending steps run in one SQL transaction (Postgres DDL is
// transactional), so a failed upgrade leaves the database as it was. The
// transaction first takes an advisory lock on the current schema, so two
// servers (or parallel tests in one schema) starting at once migrate one
// after the other instead of racing.
//
// Adding a migration (v2, v3, ...):
//
//  1. Append a func(ctx, tx) error to migrations; never reorder or edit a
//     step that has shipped. SchemaVersion follows automatically.
//  2. Leave baselineSchema alone: it stays the v1 schema. A brand-new
//     database runs every step from 0, so it ends up with the baseline plus
//     all later migrations, exactly like an upgraded one.
//  3. Add a test that builds a database at the previous version and checks
//     the upgrade (and that a fresh database matches an upgraded one).
//  4. Add a row to the version list above and to the "Database schema"
//     section of docs/data-model.md.
//
// Opening a database whose version is newer than SchemaVersion fails with
// ErrUnsupportedSchema and leaves it untouched.

// migration upgrades the schema by one version inside a SQL transaction.
type migration func(ctx context.Context, tx *sql.Tx) error

// migrations[i] upgrades from version i to i+1. Append only.
var migrations = []migration{
	// 0 -> 1: baseline schema (empty database only).
	createBaseline,
	// 1 -> 2: drop the unique index on balance names.
	allowDuplicateBalanceNames,
}

// SchemaVersion is the version a fully migrated database has.
var SchemaVersion = len(migrations)

// ErrUnsupportedSchema is returned by Open for a database created by a newer
// build (its schema version is above SchemaVersion). It is not modified.
var ErrUnsupportedSchema = errors.New("unsupported database schema")

func migrate(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// One migrator per schema at a time; released at commit or rollback.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('expensetrackr.migrate.' || current_schema()))`); err != nil {
		return fmt.Errorf("migrate: lock: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    integer     PRIMARY KEY CHECK (version >= 1),
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	var version int
	if err := tx.QueryRowContext(ctx, `SELECT coalesce(max(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version > SchemaVersion {
		return fmt.Errorf("%w: database schema version %d is newer than this binary supports (%d)",
			ErrUnsupportedSchema, version, SchemaVersion)
	}
	if version == SchemaVersion {
		return nil // up to date; the deferred rollback releases the lock
	}
	for v := version; v < SchemaVersion; v++ {
		if err := migrations[v](ctx, tx); err != nil {
			return fmt.Errorf("migration %d: %w", v+1, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, v+1); err != nil {
			return fmt.Errorf("migration %d: record version: %w", v+1, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migrate: commit: %w", err)
	}
	log.Printf("store: migrated schema from version %d to %d", version, SchemaVersion)
	return nil
}

// createBaseline is migration 0 -> 1: the full baseline schema.
func createBaseline(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, baselineSchema); err != nil {
		return fmt.Errorf("create baseline schema: %w", err)
	}
	return nil
}
