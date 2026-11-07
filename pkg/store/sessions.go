package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/nutmos/expensetrackr/pkg/common_type"
	"github.com/nutmos/expensetrackr/pkg/user"
)

// ErrRegistrationClosed: first-run registration was requested but a user
// with a password already exists.
var ErrRegistrationClosed = errors.New("registration is closed")

// Session is a stored login session. The raw token is never stored.
type Session struct {
	ID         int64
	TokenHash  string
	UserUID    string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastSeenAt time.Time
	UserAgent  string
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// HasPasswordUser reports whether any user has a password set (i.e. the
// first-run setup is done).
func (s *Store) HasPasswordUser(ctx context.Context) (bool, error) {
	var has bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE password_hash IS NOT NULL)`).Scan(&has); err != nil {
		return false, fmt.Errorf("check users: %w", err)
	}
	return has, nil
}

// GetUserByUsername looks a user up by (already lowercased) username.
func (s *Store) GetUserByUsername(ctx context.Context, username string) (user.User, error) {
	u, err := scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE lower(username) = lower($1)`, username))
	if errors.Is(err, sql.ErrNoRows) {
		return user.User{}, ErrNotFound
	}
	return u, err
}

// RegisterUser creates a validated user with a password hash in one
// transaction. With firstRun set it fails with ErrRegistrationClosed if a
// user with a password already exists (checked inside the same transaction).
func (s *Store) RegisterUser(ctx context.Context, u *user.User, hash string, firstRun bool) error {
	uid, err := common_type.NewUID()
	if err != nil {
		return err
	}
	now := ts(time.Now())
	var id int64
	err = s.inTx(ctx, "register", func(tx *sql.Tx) error {
		if firstRun {
			// Serialized with other changes to who can log in (see
			// lockLoginUsers), so two first-run registrations can't both pass.
			if err := lockLoginUsers(ctx, tx); err != nil {
				return fmt.Errorf("register: %w", err)
			}
			var has bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE password_hash IS NOT NULL)`).Scan(&has); err != nil {
				return fmt.Errorf("register: %w", err)
			}
			if has {
				return ErrRegistrationClosed
			}
		}
		err := tx.QueryRowContext(ctx,
			`INSERT INTO users (uid, username, email, display_name, preferences, password_hash, password_updated_at, status, created_at)
			 VALUES ($1, $2, NULL, $3, $4, $5, $6, 'active', $7) RETURNING id`,
			uid, nullStr(u.Username), u.DisplayName, u.Preferences.StoredJSON(), hash, now, now).Scan(&id)
		if err != nil {
			if m := mapUserErr(err); m != err {
				return m
			}
			return fmt.Errorf("register: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	u.ID, u.UID, u.CreatedAt, u.Status = id, uid.String(), now, user.StatusActive
	u.PasswordHash, u.PasswordUpdated, u.HasPassword = &hash, &now, true
	return nil
}

// RecordLogin stamps users.last_login_at.
func (s *Store) RecordLogin(ctx context.Context, uid string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET last_login_at = $1 WHERE uid = $2`, ts(at), uid)
	return err
}

// CreateSession stores a new session.
func (s *Store) CreateSession(ctx context.Context, se *Session) error {
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO sessions (token_hash, user_uid, created_at, expires_at, last_seen_at, user_agent) VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		se.TokenHash, se.UserUID, ts(se.CreatedAt), ts(se.ExpiresAt), ts(se.LastSeenAt), nullIfEmpty(se.UserAgent)).Scan(&se.ID)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// GetSession returns the session with this token hash, or ErrNotFound.
// Expired sessions are returned too; the caller checks ExpiresAt.
func (s *Store) GetSession(ctx context.Context, tokenHash string) (Session, error) {
	var se Session
	var ua sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT id, token_hash, user_uid, created_at, expires_at, last_seen_at, user_agent FROM sessions WHERE token_hash = $1`, tokenHash).
		Scan(&se.ID, &se.TokenHash, &se.UserUID, &se.CreatedAt, &se.ExpiresAt, &se.LastSeenAt, &ua)
	if errors.Is(err, sql.ErrNoRows) {
		return se, ErrNotFound
	}
	if err != nil {
		return se, fmt.Errorf("get session: %w", err)
	}
	se.CreatedAt, se.ExpiresAt, se.LastSeenAt = se.CreatedAt.UTC(), se.ExpiresAt.UTC(), se.LastSeenAt.UTC()
	se.UserAgent = ua.String
	return se, nil
}

// TouchSession slides a session's expiry.
func (s *Store) TouchSession(ctx context.Context, id int64, seen, expires time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET last_seen_at = $1, expires_at = $2 WHERE id = $3`, ts(seen), ts(expires), id)
	return err
}

// DeleteSession removes the session with this token hash (no error if none).
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = $1`, tokenHash)
	return err
}

// DeleteExpiredSessions removes sessions that expired before now.
func (s *Store) DeleteExpiredSessions(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < $1`, ts(now))
	return err
}

// lockLoginUsers serializes, across connections and servers, the
// transactions that change whether anyone can log in: first-run
// registration, and removing a user or password (the last-login-user rule,
// issue #77). Held until the transaction ends.
func lockLoginUsers(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('expensetrackr.login-users'))`)
	return err
}

// ChangePassword sets a new hash and deletes all of the user's sessions
// except keepTokenHash, in one transaction.
func (s *Store) ChangePassword(ctx context.Context, uid, hash, keepTokenHash string) error {
	return s.inTx(ctx, "change password", func(tx *sql.Tx) error {
		now := ts(time.Now())
		res, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = $1, password_updated_at = $2, updated_at = $3 WHERE uid = $4`, hash, now, now, uid)
		if err != nil {
			return fmt.Errorf("change password: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_uid = $1 AND token_hash <> $2`, uid, keepTokenHash); err != nil {
			return fmt.Errorf("change password: %w", err)
		}
		return nil
	})
}
