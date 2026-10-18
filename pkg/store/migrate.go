package store

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
)

// Schema versioning.
//
// The schema version is tracked in SQLite's PRAGMA user_version, and every
// database created by this code is tagged with PRAGMA application_id =
// applicationID so it can be told apart from databases created before the
// schema was reset to a single baseline (those used user_version 1..18 for a
// different layout and have application_id 0).
//
//	v1  baseline: the complete schema in baselineSchema (schema.go)
//
// migrations[i] upgrades a database from user_version i to i+1; migrations[0]
// creates the baseline in an empty database. Each step runs in its own SQL
// transaction together with the user_version bump, so an interrupted upgrade
// rolls back to the previous version.
//
// Adding a migration (v2, v3, ...):
//
//  1. Append a func(tx *sql.Tx) error to migrations; never reorder or edit a
//     step that has shipped. SchemaVersion follows automatically.
//  2. Leave baselineSchema alone: it stays the v1 schema. A brand-new
//     database runs every step from 0, so it ends up with the baseline plus
//     all later migrations, exactly like an upgraded one.
//  3. Add a test that builds a database at the previous version and checks
//     the upgrade (and that a fresh database matches an upgraded one).
//  4. Add a row to the version list above and to the README section
//     "Database schema".
//
// SQLite cannot alter a CHECK constraint or column type in place; such a
// change needs a table rebuild (create new table, INSERT ... SELECT, drop,
// rename, recreate indexes, keep sqlite_sequence) inside the migration.
//
// Opening a database that is not empty but lacks applicationID, or whose
// version is newer than SchemaVersion, fails with ErrUnsupportedSchema and
// leaves the file untouched.

// applicationID marks databases created by this code ("EXTR").
const applicationID = 0x45585452

// migration upgrades the schema by one version inside a SQL transaction.
type migration func(tx *sql.Tx) error

// migrations[i] upgrades from user_version i to i+1. Append only.
var migrations = []migration{
	// 0 -> 1: baseline schema (empty database only).
	createBaseline,
}

// SchemaVersion is the user_version a fully migrated database has.
var SchemaVersion = len(migrations)

// ErrUnsupportedSchema is returned by Open for a database this code cannot
// use: one created before the schema baseline reset (no upgrade path), one
// from a newer build, or a non-empty file that is not an expense database.
// The file is not modified.
var ErrUnsupportedSchema = errors.New("unsupported database schema")

func migrate(db *sql.DB) error {
	var version, appID int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if err := db.QueryRow(`PRAGMA application_id`).Scan(&appID); err != nil {
		return fmt.Errorf("read application id: %w", err)
	}
	if appID != applicationID {
		var objects int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master`).Scan(&objects); err != nil {
			return fmt.Errorf("inspect schema: %w", err)
		}
		if version != 0 || objects != 0 || appID != 0 {
			return fmt.Errorf("%w: found schema version %d (application_id %d), but this build only "+
				"supports databases created with the baseline schema (version 1 and later); old "+
				"databases are not upgraded. Delete the database file (and its -wal and -shm files) "+
				"and restart to create a new one", ErrUnsupportedSchema, version, appID)
		}
		// Empty database: fall through and create it from version 0.
	} else if version < 1 {
		return fmt.Errorf("%w: database is tagged as an expense database but has schema version %d; "+
			"delete it and restart to create a new one", ErrUnsupportedSchema, version)
	}
	if version > SchemaVersion {
		return fmt.Errorf("%w: database schema version %d is newer than this binary supports (%d)",
			ErrUnsupportedSchema, version, SchemaVersion)
	}

	for v := version; v < SchemaVersion; v++ {
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("migration %d: %w", v+1, err)
		}
		if err := migrations[v](tx); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %d: %w", v+1, err)
		}
		if err := setVersion(tx, v+1); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %d: %w", v+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("migration %d: commit: %w", v+1, err)
		}
		log.Printf("store: migrated schema to version %d", v+1)
	}
	return nil
}

// createBaseline is migration 0 -> 1: the full baseline schema, plus the
// application_id tag, in an empty database.
func createBaseline(tx *sql.Tx) error {
	if _, err := tx.Exec(baselineSchema); err != nil {
		return fmt.Errorf("create baseline schema: %w", err)
	}
	// PRAGMA does not accept bound parameters; applicationID is a constant.
	if _, err := tx.Exec(fmt.Sprintf(`PRAGMA application_id = %d`, applicationID)); err != nil {
		return fmt.Errorf("set application id: %w", err)
	}
	return nil
}

func setVersion(tx *sql.Tx, v int) error {
	// PRAGMA does not accept bound parameters; v is a trusted integer.
	if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, v)); err != nil {
		return fmt.Errorf("set schema version: %w", err)
	}
	return nil
}
