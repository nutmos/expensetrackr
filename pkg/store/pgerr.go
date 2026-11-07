package store

import (
	"database/sql"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// Postgres error codes (SQLSTATE) the store reacts to.
const (
	pgUniqueViolation     = "23505"
	pgForeignKeyViolation = "23503"
	pgInvalidText         = "22P02" // e.g. a malformed uuid
)

// pgError returns the Postgres error inside err, if any.
func pgError(err error) (*pgconn.PgError, bool) {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe, true
	}
	return nil, false
}

// isUniqueViolation reports whether err violates the unique constraint or
// index named constraint (any unique one if constraint is "").
func isUniqueViolation(err error, constraint string) bool {
	pe, ok := pgError(err)
	return ok && pe.Code == pgUniqueViolation && (constraint == "" || pe.ConstraintName == constraint)
}

// isForeignKeyViolation reports whether err violates a foreign key.
func isForeignKeyViolation(err error) bool {
	pe, ok := pgError(err)
	return ok && pe.Code == pgForeignKeyViolation
}

// isInvalidText reports a value Postgres could not parse for its column
// type, such as a uid that is not a UUID.
func isInvalidText(err error) bool {
	pe, ok := pgError(err)
	return ok && pe.Code == pgInvalidText
}

// nullTS formats a nullable timestamptz like ts (RFC 3339, UTC, seconds);
// nil when NULL.
func nullTS(t sql.NullTime) *string {
	if !t.Valid {
		return nil
	}
	s := ts(t.Time)
	return &s
}

// dateOnly formats a date column as YYYY-MM-DD.
func dateOnly(t time.Time) string { return t.UTC().Format(time.DateOnly) }
