package user

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/nutmos/expensetrackr/pkg/money"
	"github.com/nutmos/expensetrackr/pkg/validate"
)

// DefaultBaseCurrency is the base currency of a user who has not chosen one.
// The server has no other notion of a default currency (balances and
// transactions always carry their own), so a neutral, widely used code is
// used rather than guessing from the user's data.
const DefaultBaseCurrency = "USD"

// UserPreferences are a user's settings, stored as one JSON object in
// users.preferences. Add new keys as fields with a json tag (and, if they
// need one, a default in WithDefaults); old rows simply lack the key.
//
// Reading from the database is tolerant: unknown keys (e.g. written by a
// newer or older version) are ignored and missing keys get their defaults.
// API input is strict: unknown keys are rejected so typos surface.
type UserPreferences struct {
	// BaseCurrency is an ISO 4217 code from money.Currencies (the list served
	// by /api/currencies). Empty means "not set" (DefaultBaseCurrency).
	BaseCurrency string `json:"base_currency,omitempty"`
}

// WithDefaults returns p with unset keys filled in. API responses use it so
// clients always see the effective value.
func (p UserPreferences) WithDefaults() UserPreferences {
	if p.BaseCurrency == "" {
		p.BaseCurrency = DefaultBaseCurrency
	}
	return p
}

// Normalize upper-cases and trims codes and checks every key. Field errors
// are keyed by preference name (e.g. "base_currency").
func (p UserPreferences) Normalize() (UserPreferences, error) {
	fields := map[string]string{}
	p.BaseCurrency = strings.ToUpper(strings.TrimSpace(p.BaseCurrency))
	if _, ok := money.MinorUnits(p.BaseCurrency); p.BaseCurrency != "" && !ok {
		fields["base_currency"] = "must be a supported ISO 4217 currency code (see /api/currencies)"
	}
	if len(fields) > 0 {
		return UserPreferences{}, &validate.ValidationError{Fields: fields}
	}
	return p, nil
}

// ParseStoredPreferences decodes the stored column. Unknown keys are
// ignored; an empty value is treated as {}.
func ParseStoredPreferences(raw []byte) (UserPreferences, error) {
	var p UserPreferences
	if len(bytes.TrimSpace(raw)) == 0 {
		return p, nil
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return UserPreferences{}, fmt.Errorf("decode preferences: %w", err)
	}
	return p, nil
}

// DecodePreferences parses API input strictly: absent or null means {} (all
// defaults), anything else must be a JSON object with only known keys.
// Returns a ValidationError (keyed "preferences" for shape problems, or by
// preference name) on bad input.
func DecodePreferences(raw json.RawMessage) (UserPreferences, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return UserPreferences{}, nil
	}
	if len(trimmed) > MaxPreferencesSize {
		return UserPreferences{}, shapeErr(fmt.Sprintf("must be at most %d bytes", MaxPreferencesSize))
	}
	if trimmed[0] != '{' {
		return UserPreferences{}, shapeErr("must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	var p UserPreferences
	if err := dec.Decode(&p); err != nil {
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) && te.Field != "" {
			return UserPreferences{}, &validate.ValidationError{Fields: map[string]string{te.Field: "has the wrong type"}}
		}
		return UserPreferences{}, shapeErr("must be a JSON object with only known keys (" + strings.TrimPrefix(err.Error(), "json: ") + ")")
	}
	return p.Normalize()
}

func shapeErr(msg string) error {
	return &validate.ValidationError{Fields: map[string]string{"preferences": msg}}
}

// storedPreferences has Preferences' fields without its MarshalJSON, so the
// column keeps only what the user set (defaults can change later).
type storedPreferences UserPreferences

// MarshalJSON writes the effective preferences (defaults filled in), which is
// what API clients see.
func (p UserPreferences) MarshalJSON() ([]byte, error) {
	return json.Marshal(storedPreferences(p.WithDefaults()))
}

// StoredJSON is the value written to users.preferences: a JSON object with
// only the keys the user set ("{}" if none).
func (p UserPreferences) StoredJSON() string {
	b, _ := json.Marshal(storedPreferences(p)) // a struct of strings cannot fail
	return string(b)
}
