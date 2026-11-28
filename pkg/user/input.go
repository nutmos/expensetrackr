package user

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/nutmos/expensetrackr/pkg/validate"
)

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

// Validate checks the input and returns a normalized User (no IDs/times;
// Status active). Username/email "" or null are stored as NULL.
func (in Input) Validate() (User, error) {
	fieldsErr := map[string]string{}
	var u User

	if err := u.SetDisplayName(in.DisplayName); err != nil {
		fieldsErr["display_name"] = err.Error()
	}

	if err := u.SetUsername(in.Username); err != nil {
		fieldsErr["username"] = err.Error()
	}

	if err := u.SetEmail(in.Email); err != nil {
		fieldsErr["email"] = err.Error()
	}

	prefs, err := DecodePreferences(in.Preferences)
	var ve *validate.ValidationError
	if errors.As(err, &ve) {
		for k, v := range ve.Fields {
			if k != "preferences" {
				k = "preferences." + k
			}
			fieldsErr[k] = v
		}
	} else if err != nil {
		return User{}, err
	}
	u.Preferences = prefs

	u.SetStatus(StatusActive)

	if len(fieldsErr) > 0 {
		return User{}, &validate.ValidationError{Fields: fieldsErr}
	}
	return u, nil
}

// Input returns the editable fields of a stored user, for PATCH merging.
func (u User) Input() Input {
	return Input{Username: u.Username, Email: u.Email, DisplayName: u.DisplayName, Preferences: json.RawMessage(u.Preferences.StoredJSON())}
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
