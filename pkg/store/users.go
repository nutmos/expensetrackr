package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nutmos/expensetrackr/pkg/user"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

var (
	// ErrDuplicateUsername: another user has this username (case-insensitive).
	ErrDuplicateUsername = errors.New("a user with this username already exists")
	// ErrDuplicateEmail: another user has this email (case-insensitive).
	ErrDuplicateEmail = errors.New("a user with this email already exists")
	// ErrDuplicateIdentity: (provider, provider_subject) is already linked.
	ErrDuplicateIdentity = errors.New("this provider account is already linked to a user")
)

const userCols = `id, uid, username, email, email_verified_at, display_name, preferences,
	password_hash, password_updated_at, status, last_login_at, created_at, updated_at`

func mapUserErr(err error) error {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return err
	}
	switch se.Code() {
	case sqlite3.SQLITE_CONSTRAINT_UNIQUE:
		msg := se.Error()
		switch {
		case strings.Contains(msg, "users.username"):
			return ErrDuplicateUsername
		case strings.Contains(msg, "users.email"):
			return ErrDuplicateEmail
		case strings.Contains(msg, "user_identities.provider"):
			return ErrDuplicateIdentity
		}
	case sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY:
		return ErrNotFound // identity for a user that does not exist
	}
	return err
}

func nullStr(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

// CreateUser inserts a validated profile with a new UUID v4 uid. Auth fields
// (password hash, verification, last login) start empty; status is active.
func (s *Store) CreateUser(ctx context.Context, u *user.User) error {
	uid, err := newUID()
	if err != nil {
		return err
	}
	u.UID = uid
	u.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	u.UpdatedAt, u.EmailVerifiedAt, u.PasswordHash, u.PasswordUpdated, u.LastLoginAt = nil, nil, nil, nil, nil
	u.HasPassword = false
	if u.Status == "" {
		u.Status = user.StatusActive
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users (uid, username, email, display_name, preferences, status, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		u.UID, nullStr(u.Username), nullStr(u.Email), u.DisplayName, string(u.Preferences), string(u.Status), u.CreatedAt)
	if err != nil {
		if m := mapUserErr(err); m != err {
			return m
		}
		return fmt.Errorf("insert user: %w", err)
	}
	u.ID, err = res.LastInsertId()
	return err
}

// ListUsers returns all users, oldest first.
func (s *Store) ListUsers(ctx context.Context) ([]user.User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	out := []user.User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// GetUserByUID returns one user or ErrNotFound.
func (s *Store) GetUserByUID(ctx context.Context, uid string) (user.User, error) {
	u, err := scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE uid = ?`, uid))
	if errors.Is(err, sql.ErrNoRows) {
		return user.User{}, ErrNotFound
	}
	return u, err
}

// UpdateUser loads user id, applies fn (profile fields only) and stores the
// result in one database transaction. uid, status, password and login fields
// are preserved. Changing the email clears email_verified_at.
func (s *Store) UpdateUser(ctx context.Context, id int64, fn func(cur user.User) (user.User, error)) (user.User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return user.User{}, fmt.Errorf("update user: %w", err)
	}
	defer tx.Rollback()
	cur, err := scanUser(tx.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return user.User{}, ErrNotFound
	}
	if err != nil {
		return user.User{}, err
	}
	next, err := fn(cur)
	if err != nil {
		return user.User{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	// Keep everything the profile API does not own.
	next.ID, next.UID, next.CreatedAt, next.UpdatedAt = cur.ID, cur.UID, cur.CreatedAt, &now
	next.Status, next.PasswordHash, next.PasswordUpdated, next.LastLoginAt = cur.Status, cur.PasswordHash, cur.PasswordUpdated, cur.LastLoginAt
	next.HasPassword = cur.PasswordHash != nil
	next.EmailVerifiedAt = cur.EmailVerifiedAt
	if !sameStr(next.Email, cur.Email) {
		next.EmailVerifiedAt = nil // a new address is unverified
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE users SET username = ?, email = ?, email_verified_at = ?, display_name = ?, preferences = ?, updated_at = ?
		 WHERE id = ?`,
		nullStr(next.Username), nullStr(next.Email), nullStr(next.EmailVerifiedAt), next.DisplayName, string(next.Preferences), now, id); err != nil {
		if m := mapUserErr(err); m != err {
			return user.User{}, m
		}
		return user.User{}, fmt.Errorf("update user: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return user.User{}, fmt.Errorf("update user: %w", err)
	}
	return next, nil
}

func sameStr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// DeleteUser removes a user; linked identities go with it (ON DELETE CASCADE).
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetPasswordHash stores (or with nil, removes) the password hash for a user
// and stamps password_updated_at. Not exposed over HTTP: reserved for the
// future login feature, which must hash passwords (e.g. argon2id) before
// calling this. The hash is never returned by the API.
func (s *Store) SetPasswordHash(ctx context.Context, uid string, hash *string) error {
	if hash != nil && *hash == "" {
		return errors.New("set password hash: empty hash")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	var stamp any = now
	if hash == nil {
		stamp = nil
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET password_hash = ?, password_updated_at = ?, updated_at = ? WHERE uid = ?`,
		nullStr(hash), stamp, now, uid)
	if err != nil {
		return fmt.Errorf("set password hash: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- SSO identities ------------------------------------------------------

const identityCols = `id, uid, user_uid, provider, provider_subject, email, created_at, last_used_at`

// CreateIdentity links a provider account to an existing user. Returns
// ErrDuplicateIdentity if (provider, provider_subject) is already linked and
// ErrNotFound if the user does not exist. Not exposed over HTTP yet (the SSO
// callback will call it).
func (s *Store) CreateIdentity(ctx context.Context, id *user.Identity) error {
	id.ProviderSubject = strings.TrimSpace(id.ProviderSubject)
	if err := id.Validate(); err != nil {
		return err
	}
	uid, err := newUID()
	if err != nil {
		return err
	}
	id.UID = uid
	id.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	id.LastUsedAt = nil
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO user_identities (uid, user_uid, provider, provider_subject, email, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id.UID, id.UserUID, string(id.Provider), id.ProviderSubject, nullStr(id.Email), id.CreatedAt)
	if err != nil {
		if m := mapUserErr(err); m != err {
			return m
		}
		return fmt.Errorf("insert identity: %w", err)
	}
	id.ID, err = res.LastInsertId()
	return err
}

// ListIdentities returns the identities linked to a user (oldest first).
func (s *Store) ListIdentities(ctx context.Context, userUID string) ([]user.Identity, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+identityCols+` FROM user_identities WHERE user_uid = ? ORDER BY id`, userUID)
	if err != nil {
		return nil, fmt.Errorf("list identities: %w", err)
	}
	defer rows.Close()
	out := []user.Identity{}
	for rows.Next() {
		var id user.Identity
		var provider string
		var email, used sql.NullString
		if err := rows.Scan(&id.ID, &id.UID, &id.UserUID, &provider, &id.ProviderSubject, &email, &id.CreatedAt, &used); err != nil {
			return nil, fmt.Errorf("scan identity: %w", err)
		}
		id.Provider = user.Provider(provider)
		id.Email = strPtr(email)
		id.LastUsedAt = strPtr(used)
		out = append(out, id)
	}
	return out, rows.Err()
}

// FindUserByIdentity returns the user linked to (provider, subject), or
// ErrNotFound. For the future SSO sign-in.
func (s *Store) FindUserByIdentity(ctx context.Context, provider user.Provider, subject string) (user.User, error) {
	u, err := scanUser(s.db.QueryRowContext(ctx,
		`SELECT `+prefixed("u.", userCols)+` FROM users u
		 JOIN user_identities i ON i.user_uid = u.uid
		 WHERE i.provider = ? AND i.provider_subject = ?`, string(provider), subject))
	if errors.Is(err, sql.ErrNoRows) {
		return user.User{}, ErrNotFound
	}
	return u, err
}

func prefixed(p, cols string) string {
	parts := strings.Split(cols, ",")
	for i, c := range parts {
		parts[i] = p + strings.TrimSpace(c)
	}
	return strings.Join(parts, ", ")
}

func strPtr(n sql.NullString) *string {
	if !n.Valid {
		return nil
	}
	s := n.String
	return &s
}

func scanUser(r scanner) (user.User, error) {
	var u user.User
	var username, email, verified, hash, pwUpdated, lastLogin, updated sql.NullString
	var prefs, status string
	if err := r.Scan(&u.ID, &u.UID, &username, &email, &verified, &u.DisplayName, &prefs,
		&hash, &pwUpdated, &status, &lastLogin, &u.CreatedAt, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return u, err
		}
		return u, fmt.Errorf("scan user: %w", err)
	}
	u.Username, u.Email, u.EmailVerifiedAt = strPtr(username), strPtr(email), strPtr(verified)
	u.PasswordHash, u.PasswordUpdated, u.LastLoginAt, u.UpdatedAt = strPtr(hash), strPtr(pwUpdated), strPtr(lastLogin), strPtr(updated)
	u.HasPassword = u.PasswordHash != nil
	u.Preferences = []byte(prefs)
	u.Status = user.Status(status)
	return u, nil
}
