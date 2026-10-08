// Package user models user profiles: a uid, a display name, optional login
// identifiers (username, email) and free-form preferences stored as a JSON
// object.
//
// The record also has fields reserved for authentication, which is not
// implemented yet: password_hash (basic login) and linked SSO identities
// (Sign in with Apple / Google, see Identity). The API never returns secrets.
// It only says whether a password is set (has_password).
package user

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/mail"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/nutmos/expensetrackr/pkg/validate"
)

// Limits.
const (
	MaxDisplayNameLen  = 100
	MaxEmailLen        = 254
	MaxPreferencesSize = 32 << 10 // bytes of the compacted JSON object (requests are capped at 64 KiB)
)

// Status of an account. Only "active" is used until auth exists; "disabled"
// is reserved for blocking sign-in later.
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
	ID              int64           `json:"-"`   // internal row id; the API uses UID
	UID             string          `json:"uid"` // server-assigned UUID v4; immutable
	Username        *string         `json:"username"`
	Email           *string         `json:"email"`
	EmailVerifiedAt *string         `json:"email_verified_at"` // set by a future verification flow
	DisplayName     string          `json:"display_name"`
	Preferences     json.RawMessage `json:"preferences"` // always a JSON object
	Status          Status          `json:"status"`
	HasPassword     bool            `json:"has_password"` // derived: PasswordHash != nil
	PasswordHash    *string         `json:"-"`            // future basic auth (e.g. argon2id PHC string); NEVER exposed
	PasswordUpdated *string         `json:"password_updated_at"`
	LastLoginAt     *string         `json:"last_login_at"`
	CreatedAt       string          `json:"created_at"`
	UpdatedAt       *string         `json:"updated_at"`
}

// Input is the payload for POST /api/users and PUT /api/users/:uid (and the
// merge target for PATCH). Read-only fields (uid, status, timestamps,
// has_password, email_verified_at) are accepted so a GET response can be sent
// back, but they are ignored. password/password_hash are not accepted at all.
type Input struct {
	Username    *string         `json:"username"`
	Email       *string         `json:"email"`
	DisplayName string          `json:"display_name"`
	Preferences json.RawMessage `json:"preferences"`

	IgnoredUID             string  `json:"uid,omitempty"`
	IgnoredStatus          string  `json:"status,omitempty"`
	IgnoredHasPassword     *bool   `json:"has_password,omitempty"`
	IgnoredEmailVerifiedAt *string `json:"email_verified_at,omitempty"`
	IgnoredPasswordUpdated *string `json:"password_updated_at,omitempty"`
	IgnoredLastLoginAt     *string `json:"last_login_at,omitempty"`
	IgnoredCreatedAt       *string `json:"created_at,omitempty"`
	IgnoredUpdatedAt       *string `json:"updated_at,omitempty"`
}

// NormalizePreferences checks that raw is a JSON object of at most
// MaxPreferencesSize bytes and returns it compacted. Absent or null → {}.
func NormalizePreferences(raw json.RawMessage) (json.RawMessage, string) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return json.RawMessage(`{}`), ""
	}
	if trimmed[0] != '{' {
		return nil, "must be a JSON object"
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, trimmed); err != nil {
		return nil, "must be valid JSON"
	}
	// Compact accepts any JSON value; make sure it is really one object.
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(buf.Bytes(), &obj); err != nil || obj == nil {
		return nil, "must be a JSON object"
	}
	if buf.Len() > MaxPreferencesSize {
		return nil, fmt.Sprintf("must be at most %d bytes", MaxPreferencesSize)
	}
	return json.RawMessage(buf.Bytes()), ""
}

// NormalizeEmail trims and lowercases an address and checks it is a bare
// addr-spec (no display name). "" means "no email".
func NormalizeEmail(s string) (string, string) {
	e := strings.ToLower(strings.TrimSpace(s))
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
func NormalizeUsername(s string) (string, string) {
	u := strings.ToLower(strings.TrimSpace(s))
	if u == "" {
		return "", ""
	}
	if !usernameRE.MatchString(u) {
		return "", "must be 3-32 characters: letters, digits, '.', '_' or '-', starting with a letter or digit"
	}
	return u, ""
}

// Validate checks the input and returns a normalized User (no IDs/times;
// Status active). Username/email "" or null are stored as NULL.
func (in Input) Validate() (User, error) {
	fields := map[string]string{}
	var u User

	name := strings.TrimSpace(in.DisplayName)
	switch {
	case name == "":
		fields["display_name"] = "is required"
	case utf8.RuneCountInString(name) > MaxDisplayNameLen:
		fields["display_name"] = fmt.Sprintf("must be at most %d characters", MaxDisplayNameLen)
	}
	u.DisplayName = name

	if in.Username != nil {
		if v, msg := NormalizeUsername(*in.Username); msg != "" {
			fields["username"] = msg
		} else if v != "" {
			u.Username = &v
		}
	}
	if in.Email != nil {
		if v, msg := NormalizeEmail(*in.Email); msg != "" {
			fields["email"] = msg
		} else if v != "" {
			u.Email = &v
		}
	}
	prefs, msg := NormalizePreferences(in.Preferences)
	if msg != "" {
		fields["preferences"] = msg
	}
	u.Preferences = prefs
	u.Status = StatusActive

	if len(fields) > 0 {
		return User{}, &validate.ValidationError{Fields: fields}
	}
	return u, nil
}

// Input returns the editable fields of a stored user, for PATCH merging.
func (u User) Input() Input {
	return Input{Username: u.Username, Email: u.Email, DisplayName: u.DisplayName, Preferences: u.Preferences}
}

var patchFields = map[string]bool{"username": true, "email": true, "display_name": true, "preferences": true}

// readOnly fields may appear in a PATCH (round-tripping a GET response) and
// are ignored.
var readOnly = []string{"uid", "status", "has_password", "email_verified_at", "password_updated_at", "last_login_at", "created_at", "updated_at"}

// ApplyPatch overlays the fields present in patch onto in:
//   - "username"/"email": a string sets it, null or "" removes it
//   - "display_name": string; null is a validation error
//   - "preferences": REPLACES the whole object (no deep merge); null resets to {}
//
// Unknown fields (including password / password_hash) are a request error.
func (in *Input) ApplyPatch(patch map[string]json.RawMessage) error {
	for _, k := range readOnly {
		delete(patch, k)
	}
	if len(patch) == 0 {
		return &validate.RequestError{Msg: "patch must contain at least one of: display_name, username, email, preferences"}
	}
	var unknown []string
	for k := range patch {
		if !patchFields[k] {
			unknown = append(unknown, fmt.Sprintf("%q", k))
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return &validate.RequestError{Msg: "unknown field " + strings.Join(unknown, ", ")}
	}
	isNull := func(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

	if raw, ok := patch["display_name"]; ok {
		if isNull(raw) {
			return &validate.ValidationError{Fields: map[string]string{"display_name": "cannot be null"}}
		}
		if err := json.Unmarshal(raw, &in.DisplayName); err != nil {
			return &validate.RequestError{Msg: `field "display_name" must be a string`}
		}
	}
	for _, f := range []struct {
		name string
		dst  **string
	}{{"username", &in.Username}, {"email", &in.Email}} {
		raw, ok := patch[f.name]
		if !ok {
			continue
		}
		if isNull(raw) {
			*f.dst = nil
			continue
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return &validate.RequestError{Msg: fmt.Sprintf("field %q must be a string or null", f.name)}
		}
		*f.dst = &s
	}
	if raw, ok := patch["preferences"]; ok {
		in.Preferences = raw
	}
	return nil
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
	switch {
	case sub == "":
		fields["provider_subject"] = "is required"
	case len(sub) > MaxSubjectLen:
		fields["provider_subject"] = fmt.Sprintf("must be at most %d characters", MaxSubjectLen)
	}
	if len(fields) > 0 {
		return &validate.ValidationError{Fields: fields}
	}
	return nil
}
