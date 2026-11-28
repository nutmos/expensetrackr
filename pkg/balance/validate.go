package balance

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/nutmos/expensetrackr/pkg/money"
	"github.com/nutmos/expensetrackr/pkg/validate"
)

// ErrTypeChanged is the field message for an update that tries to change type.
const ErrTypeChanged = "balance type cannot be changed after creation"

// Which types use an amount, for "is not used for type ..." messages.
const (
	usedByAssets      = "payment_account and other_asset"
	usedByLiabilities = "credit_card and other_liability"
)

// Validate checks the input and returns a normalized Balance (without ID and
// timestamps), or a *validate.ValidationError listing every invalid field.
func (in Input) Validate() (Balance, error) {
	v := validator{fields: map[string]string{}}
	b := Balance{
		Name:        v.name(in.Name),
		Type:        v.typ(in.Type),
		Currency:    v.currency(in.Currency),
		Description: v.description(in.Description),
	}
	v.balanceType = b.Type
	v.scale, v.currencyKnown = money.MinorUnits(b.Currency)

	var bal, debt, limit *int64
	if b.Type.IsAsset() {
		bal = v.amount("balance", in.Balance, money.ParseSigned)
		v.notUsed("debt", in.Debt, usedByLiabilities)
		v.notUsed("limit", in.Limit, usedByLiabilities)
	} else if b.Type.IsLiability() {
		// Debt may be negative: an overpaid card or loan is in credit.
		debt = v.amount("debt", in.Debt, money.ParseSigned)
		limit = v.amount("limit", in.Limit, money.ParseNonNegative)
		v.notUsed("balance", in.Balance, usedByAssets)
	}

	if err := v.err(); err != nil {
		return Balance{}, err
	}
	b.SetAmounts(v.scale, bal, debt, limit)
	return b, nil
}

// ValidateUpdate validates in as the new state of a stored balance of type
// stored (PUT). The type is fixed at creation: in must carry the same type
// (case/space-insensitive); a different one is a validation error on "type".
// The other fields are then still checked against the stored type, so every
// problem is reported at once.
func (in Input) ValidateUpdate(stored Type) (Balance, error) {
	t := ParseType(in.Type)
	if t == "" || t == stored {
		return in.Validate()
	}
	in.Type = string(stored)
	fields := map[string]string{}
	if _, err := in.Validate(); err != nil {
		var ve *validate.ValidationError
		if !errors.As(err, &ve) {
			return Balance{}, err
		}
		fields = ve.Fields
	}
	fields["type"] = ErrTypeChanged
	return Balance{}, &validate.ValidationError{Fields: fields}
}

// validator normalizes Input fields and collects one message per bad field.
type validator struct {
	fields        map[string]string
	balanceType   Type // set once the type is known, for amount messages
	scale         int
	currencyKnown bool
}

func (v *validator) fail(field, msg string) { v.fields[field] = msg }

func (v *validator) err() error {
	if len(v.fields) == 0 {
		return nil
	}
	return &validate.ValidationError{Fields: v.fields}
}

func (v *validator) name(in string) string {
	s := strings.TrimSpace(in)
	if s == "" {
		v.fail("name", "is required")
	} else if utf8.RuneCountInString(s) > MaxNameLen {
		v.fail("name", fmt.Sprintf("must be at most %d characters", MaxNameLen))
	}
	return s
}

func (v *validator) typ(in string) Type {
	t := ParseType(in)
	if t == "" {
		v.fail("type", "is required (one of "+TypeList()+")")
	} else if !t.Valid() {
		v.fail("type", fmt.Sprintf("%q is not valid; use one of %s", in, TypeList()))
	}
	return t
}

func (v *validator) currency(in string) string {
	c := strings.ToUpper(strings.TrimSpace(in))
	if c == "" {
		v.fail("currency", "is required")
	} else if _, known := money.MinorUnits(c); !known {
		v.fail("currency", fmt.Sprintf("%q is not a recognised ISO 4217 currency code", in))
	}
	return c
}

func (v *validator) description(in string) string {
	s := strings.TrimSpace(in)
	if utf8.RuneCountInString(s) > MaxDescriptionLen {
		v.fail("description", fmt.Sprintf("must be at most %d characters", MaxDescriptionLen))
	}
	return s
}

// amount parses a required amount field with parse; it returns nil if the
// field is absent or invalid (recording why), or if the currency is unknown
// (decimals can't be checked; the currency error is already recorded).
func (v *validator) amount(field string, d *money.DecimalInput, parse func(string, int) (int64, error)) *int64 {
	if !provided(d) {
		v.fail(field, "is required for type "+string(v.balanceType))
		return nil
	}
	if !v.currencyKnown {
		return nil
	}
	n, err := parse(string(*d), v.scale)
	if err != nil {
		v.fail(field, err.Error())
		return nil
	}
	return &n
}

// notUsed records an error if field was sent for a type that has no such amount.
func (v *validator) notUsed(field string, d *money.DecimalInput, usedBy string) {
	if provided(d) {
		v.fail(field, fmt.Sprintf("is not used for type %s (only for %s)", v.balanceType, usedBy))
	}
}
