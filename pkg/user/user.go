// Package user models user profiles: a uid, a display name, optional login
// identifiers (username, email) and typed preferences (see Preferences)
// stored as one JSON object.
//
// The record also carries authentication data: password_hash (a bcrypt hash
// for username/password login, see pkg/auth and docs/auth.md) and linked SSO
// identities (Sign in with Apple / Google, see Identity; not wired to a
// sign-in flow yet). The API never returns secrets. It only says whether a
// password is set (has_password).
package user

import (
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/nutmos/expensetrackr/pkg/helper"
	"github.com/nutmos/expensetrackr/pkg/validate"
)

// Limits.
const (
	MaxDisplayNameLen  = 100
	MaxEmailLen        = 254
	MaxPreferencesSize = 32 << 10 // bytes of the JSON object (requests are capped at 64 KiB)
)

// Status of an account. Login, sessions and API tokens require "active";
// "disabled" blocks all of them, but no API sets it yet.
type Status string

const (
	StatusActive   Status = "active"
	StatusDisabled Status = "disabled"
)

// usernameRE: 3-32 chars, lowercase letters, digits, '.', '_' or '-',
// starting with a letter or digit. Usernames are stored lowercased.
var usernameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{2,31}$`)

// User is a stored profile. PasswordHash is never serialized.
type User struct {
	ID              int64       `json:"-"`   // internal row id; the API uses UID
	UID             string      `json:"uid"` // server-assigned UUID v4; immutable
	Username        *string     `json:"username"`
	Email           *string     `json:"email"`
	EmailVerifiedAt *string     `json:"email_verified_at"` // set by a future verification flow
	DisplayName     string      `json:"display_name"`
	Preferences     Preferences `json:"preferences"` // stored as one JSON object
	Status          Status      `json:"status"`
	HasPassword     bool        `json:"has_password"` // derived: PasswordHash != nil
	PasswordHash    *string     `json:"-"`            // bcrypt hash (pkg/auth); NEVER exposed
	PasswordUpdated *string     `json:"password_updated_at"`
	LastLoginAt     *string     `json:"last_login_at"`
	CreatedAt       string      `json:"created_at"`
	UpdatedAt       *string     `json:"updated_at"`
}

func (u *User) SetDisplayName(displayName string) error {
	name := strings.TrimSpace(displayName)
	if name == "" {
		return fmt.Errorf("is required")
	} else if utf8.RuneCountInString(name) > MaxDisplayNameLen {
		return fmt.Errorf("must be at most %d characters", MaxDisplayNameLen)
	}
	u.DisplayName = name
	return nil
}

func (u *User) SetUsername(username *string) error {
	if username != nil {
		if v, msg := NormalizeUsername(*username); msg != "" {
			return fmt.Errorf("%s", msg)
		} else if v != "" {
			u.Username = &v
		}
	}
	return nil
}

func (u *User) SetEmail(email *string) error {
	if email != nil {
		if v, msg := NormalizeEmail(*email); msg != "" {
			return fmt.Errorf("%s", msg)
		} else if v != "" {
			u.Email = &v
		}
	}
	return nil
}

func (u *User) SetStatus(s Status) error {
	u.Status = StatusActive
	return nil
}

// NormalizeEmail trims and lowercases an address and checks it is a bare
// addr-spec (no display name). "" means "no email".
func NormalizeEmail(s string) (string, string) {
	e := helper.Normalize(s)
	if e == "" {
		return "", ""
	}
	if len(e) > MaxEmailLen {
		return "", fmt.Sprintf("must be at most %d characters", MaxEmailLen)
	}
	a, err := mail.ParseAddress(e)
	if err != nil || a.Address != e || a.Name != "" || !strings.Contains(e[strings.LastIndex(e, "@")+1:], ".") {
		return "", "must be a valid email address (e.g. name@example.com)"
	}
	return e, ""
}

// NormalizeUsername lowercases and checks a username. "" means "none".
func NormalizeUsername(s string) (username string, errorMsg string) {
	u := helper.Normalize(s)
	if u == "" {
		return "", ""
	}
	if !usernameRE.MatchString(u) {
		return "", "must be 3-32 characters: letters, digits, '.', '_' or '-', starting with a letter or digit"
	}
	return u, ""
}

// ---- SSO identities (for future Sign in with Apple / Google) ---------------

// Provider is an SSO identity provider.
type Provider string

const (
	ProviderApple  Provider = "apple"
	ProviderGoogle Provider = "google"
)

// Providers are the accepted providers. The database only checks the format
// (lowercase, non-empty) so adding one later needs no migration.
var Providers = []Provider{ProviderApple, ProviderGoogle}

// Valid reports whether p is an accepted provider.
func (p Provider) Valid() bool {
	for _, q := range Providers {
		if p == q {
			return true
		}
	}
	return false
}

// Identity links a user to an external account: (provider, subject) is the
// provider's stable user id (the OIDC "sub" claim), unique per provider.
type Identity struct {
	ID              int64    `json:"-"`
	UID             string   `json:"uid"`
	UserUID         string   `json:"user_uid"`
	Provider        Provider `json:"provider"`
	ProviderSubject string   `json:"provider_subject"`
	Email           *string  `json:"email"` // as reported by the provider (may be a relay address)
	CreatedAt       string   `json:"created_at"`
	LastUsedAt      *string  `json:"last_used_at"`
}

// MaxSubjectLen bounds provider_subject (Apple/Google subjects are well under).
const MaxSubjectLen = 255

// Validate checks an identity before it is stored (used by the store and,
// later, by the SSO callback).
func (id Identity) Validate() error {
	fields := map[string]string{}
	if !id.Provider.Valid() {
		fields["provider"] = "must be one of: apple, google"
	}
	sub := strings.TrimSpace(id.ProviderSubject)
	if sub == "" {
		fields["provider_subject"] = "is required"
	} else if len(sub) > MaxSubjectLen {
		fields["provider_subject"] = fmt.Sprintf("must be at most %d characters", MaxSubjectLen)
	}
	if len(fields) > 0 {
		return &validate.ValidationError{Fields: fields}
	}
	return nil
}
