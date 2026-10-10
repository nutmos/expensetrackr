package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

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
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE password_hash IS NOT NULL)`).Scan(&n); err != nil {
		return false, fmt.Errorf("check users: %w", err)
	}
	return n == 1, nil
}

// GetUserByUsername looks a user up by (already lowercased) username.
func (s *Store) GetUserByUsername(ctx context.Context, username string) (user.User, error) {
	u, err := scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE username = ? COLLATE NOCASE`, username))
	if errors.Is(err, sql.ErrNoRows) {
		return user.User{}, ErrNotFound
	}
	return u, err
}

// RegisterUser creates a validated user with a password hash in one
// transaction. With firstRun set it fails with ErrRegistrationClosed if a
// user with a password already exists (checked inside the same transaction).
func (s *Store) RegisterUser(ctx context.Context, u *user.User, hash string, firstRun bool) error {
	uid, err := newUID()
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}
	defer tx.Rollback()
	if firstRun {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE password_hash IS NOT NULL)`).Scan(&n); err != nil {
			return fmt.Errorf("register: %w", err)
		}
		if n == 1 {
			return ErrRegistrationClosed
		}
	}
	now := ts(time.Now())
	res, err := tx.ExecContext(ctx,
		`INSERT INTO users (uid, username, email, display_name, preferences, password_hash, password_updated_at, status, created_at)
		 VALUES (?, ?, NULL, ?, ?, ?, ?, 'active', ?)`,
		uid, nullStr(u.Username), u.DisplayName, string(u.Preferences), hash, now, now)
	if err != nil {
		if m := mapUserErr(err); m != err {
			return m
		}
		return fmt.Errorf("register: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("register: %w", err)
	}
	u.ID, u.UID, u.CreatedAt, u.Status = id, uid, now, user.StatusActive
	u.PasswordHash, u.PasswordUpdated, u.HasPassword = &hash, &now, true
	return nil
}

// RecordLogin stamps users.last_login_at.
func (s *Store) RecordLogin(ctx context.Context, uid string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET last_login_at = ? WHERE uid = ?`, ts(at), uid)
	return err
}

// CreateSession stores a new session.
func (s *Store) CreateSession(ctx context.Context, se *Session) error {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, user_uid, created_at, expires_at, last_seen_at, user_agent) VALUES (?, ?, ?, ?, ?, ?)`,
		se.TokenHash, se.UserUID, ts(se.CreatedAt), ts(se.ExpiresAt), ts(se.LastSeenAt), nullIfEmpty(se.UserAgent))
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	se.ID, err = res.LastInsertId()
	return err
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
	var c, e, l string
	var ua sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT id, token_hash, user_uid, created_at, expires_at, last_seen_at, user_agent FROM sessions WHERE token_hash = ?`, tokenHash).
		Scan(&se.ID, &se.TokenHash, &se.UserUID, &c, &e, &l, &ua)
	if errors.Is(err, sql.ErrNoRows) {
		return se, ErrNotFound
	}
	if err != nil {
		return se, fmt.Errorf("get session: %w", err)
	}
	se.CreatedAt, _ = time.Parse(time.RFC3339, c)
	se.ExpiresAt, _ = time.Parse(time.RFC3339, e)
	se.LastSeenAt, _ = time.Parse(time.RFC3339, l)
	se.UserAgent = ua.String
	return se, nil
}

// TouchSession slides a session's expiry.
func (s *Store) TouchSession(ctx context.Context, id int64, seen, expires time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET last_seen_at = ?, expires_at = ? WHERE id = ?`, ts(seen), ts(expires), id)
	return err
}

// DeleteSession removes the session with this token hash (no error if none).
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	return err
}

// DeleteExpiredSessions removes sessions that expired before now.
func (s *Store) DeleteExpiredSessions(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, ts(now))
	return err
}

// ChangePassword sets a new hash and deletes all of the user's sessions
// except keepTokenHash, in one transaction.
func (s *Store) ChangePassword(ctx context.Context, uid, hash, keepTokenHash string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("change password: %w", err)
	}
	defer tx.Rollback()
	now := ts(time.Now())
	res, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ?, password_updated_at = ?, updated_at = ? WHERE uid = ?`, hash, now, now, uid)
	if err != nil {
		return fmt.Errorf("change password: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_uid = ? AND token_hash <> ?`, uid, keepTokenHash); err != nil {
		return fmt.Errorf("change password: %w", err)
	}
	return tx.Commit()
}
