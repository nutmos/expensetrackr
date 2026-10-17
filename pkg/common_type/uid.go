// Package common_type holds small value types shared by the domain packages.
package common_type

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
)

const uidLen = 36 // canonical UUID form

// UID is the public identifier of a record: a UUID in canonical lowercase
// form, e.g. "a1b2c3d4-e5f6-4789-a012-3456789abcde". The zero value means
// "none".
type UID string

// NewUID returns a random UUID v4 in canonical lowercase form.
func NewUID() (UID, error) {
	id, err := uuid.NewRandom()
	if err != nil {
		return "", fmt.Errorf("generate uid: %w", err)
	}
	return UID(id.String()), nil
}

// NormalizeUID trims and lower-cases a uid as sent by a client. It does not
// check the shape; see Valid.
func NormalizeUID(s string) UID { return UID(strings.ToLower(strings.TrimSpace(s))) }

// Valid reports whether u has the canonical UUID shape (version digit not
// checked here; the store is the source of truth). The length check limits it
// to the canonical form: uuid.Validate alone also accepts the urn:uuid:,
// braced and dash-less forms.
func (u UID) Valid() bool {
	return len(u) == uidLen && uuid.Validate(string(u)) == nil
}

// Matches reports whether u and s name the same record (case-insensitive).
func (u UID) Matches(s string) bool { return strings.EqualFold(string(u), s) }

func (u UID) String() string { return string(u) }
