package balance

import (
	"errors"
	"fmt"
	"maps"
	"strings"
	"unicode/utf8"

	"github.com/nutmos/expensetrackr/pkg/common_type"
	"github.com/nutmos/expensetrackr/pkg/money"
	"github.com/nutmos/expensetrackr/pkg/validate"
)

// Input is the payload for POST and PUT /api/balances (and the merge target
// for PATCH). An amount that is absent, null or "" counts as not provided.
// UID is accepted so clients can round-trip a previous response; it is
// ignored (the server assigns and never changes it).
type Input struct {
	UID         common_type.UID     `json:"uid,omitempty"` // ignored; server-assigned
	Name        string              `json:"name"`
	Type        string              `json:"type"`
	Currency    string              `json:"currency"`
	Description string              `json:"description"`
	Balance     *money.DecimalInput `json:"balance"`
	Debt        *money.DecimalInput `json:"debt"`
	Limit       *money.DecimalInput `json:"limit"`

	// Version is the optimistic-locking version for PUT (alternative to the
	// If-Match header). Ignored on create.
	Version *int64 `json:"version,omitempty"`
}

// Input returns the editable fields of a stored balance, e.g. to build a PUT
// body from a stored record.
func (b Balance) Input() Input {
	dec := func(s *string) *money.DecimalInput {
		if s == nil {
			return nil
		}
		d := money.DecimalInput(*s)
		return &d
	}
	return Input{
		Name: b.Name, Type: string(b.Type), Currency: b.Currency, Description: b.Description,
		Balance: dec(b.Balance), Debt: dec(b.Debt), Limit: dec(b.Limit),
	}
}

func provided(d *money.DecimalInput) bool {
	return d != nil && strings.TrimSpace(string(*d)) != ""
}

// normalizeType trims and lower-cases a type as sent by a client.
func normalizeType(s string) Type { return Type(strings.ToLower(strings.TrimSpace(s))) }

// Validate checks the input and returns a normalized Balance (without ID and
// timestamps), or a *validate.ValidationError listing every invalid field.
func (in Input) Validate() (Balance, error) {
	fields := map[string]string{}
	var b Balance

	if out := b.inputNameField(in.Name); out != "" {
		fields["name"] = out
	}

	if out := b.inputTypeField(in.Type); out != "" {
		fields["type"] = out
	}

	scale, known, outCurrency := b.inputCurrencyField(in.Currency)
	if outCurrency != "" {
		fields["currency"] = outCurrency
	}

	if out := b.inputDescriptionField(in.Description); out != "" {
		fields["description"] = out
	}

	// parse validates one amount field; it returns nil if absent or invalid.
	parse := func(name string, d *money.DecimalInput, required bool, fn func(string, int) (int64, error)) *int64 {
		if !provided(d) {
			if required {
				fields[name] = "is required for type " + string(t)
			}
			return nil
		}
		if !known {
			return nil // can't check decimals without a valid currency
		}
		v, err := fn(string(*d), scale)
		if err != nil {
			fields[name] = err.Error()
			return nil
		}
		return &v
	}
	notUsed := func(name string, d *money.DecimalInput, usedBy string) {
		if provided(d) {
			fields[name] = fmt.Sprintf("is not used for type %s (only for %s)", t, usedBy)
		}
	}

	var bal, debt, limit *int64
	if b.Type.IsAsset() {
		bal = parse("balance", in.Balance, true, money.ParseSigned)
		notUsed("debt", in.Debt, "credit_card and other_liability")
		notUsed("limit", in.Limit, "credit_card and other_liability")
	} else if b.Type.IsLiability() {
		// Debt may be negative: an overpaid card or loan is in credit.
		debt = parse("debt", in.Debt, true, money.ParseSigned)
		limit = parse("limit", in.Limit, true, money.ParseNonNegative)
		notUsed("balance", in.Balance, "payment_account and other_asset")
	}

	if len(fields) > 0 {
		return Balance{}, &validate.ValidationError{Fields: fields}
	}
	b.SetAmounts(scale, bal, debt, limit)
	return b, nil
}

func (b *Balance) inputNameField(in string) string {
	b.Name = strings.TrimSpace(in)
	if b.Name == "" {
		return "is required"
	}
	if utf8.RuneCountInString(b.Name) > MaxNameLen {
		return fmt.Sprintf("must be at most %d characters", MaxNameLen)
	}
	return ""
}

func (b *Balance) inputTypeField(in string) string {
	b.Type = normalizeType(in)
	if b.Type == "" {
		return "is required (one of " + typeList() + ")"
	}
	if !b.Type.Valid() {
		return fmt.Sprintf("%q is not valid; use one of %s", in, typeList())
	}
	return ""
}

func (b *Balance) inputCurrencyField(in string) (scale int, known bool, out string) {
	b.Currency = strings.ToUpper(strings.TrimSpace(in))
	scale, known = money.MinorUnits(b.Currency)
	if b.Currency == "" {
		return scale, known, "is required"
	}
	if !known {
		return scale, known, fmt.Sprintf("%q is not a recognised ISO 4217 currency code", in.Currency)
	}
	return scale, known, ""
}

func (b *Balance) inputDescriptionField(in string) string {
	b.Description = strings.TrimSpace(in)
	if utf8.RuneCountInString(b.Description) > MaxDescriptionLen {
		return fmt.Sprintf("must be at most %d characters", MaxDescriptionLen)
	}
	return ""
}

// ErrTypeChanged is the field message for an update that tries to change type.
const ErrTypeChanged = "balance type cannot be changed after creation"

// ValidateUpdate validates in as the new state of a stored balance of type
// stored (PUT). The type is fixed at creation: in must carry the same type
// (case/space-insensitive); a different one is a validation error on "type".
// The other fields are then still checked against the stored type, so every
// problem is reported at once.
func (in Input) ValidateUpdate(stored Type) (Balance, error) {
	t := normalizeType(in.Type)
	changed := t != "" && t != stored
	if !changed {
		return in.Validate()
	}
	in.Type = string(stored)
	fields := map[string]string{}
	if _, err := in.Validate(); err != nil {
		var ve *validate.ValidationError
		if !errors.As(err, &ve) {
			return Balance{}, err
		}
		maps.Copy(fields, ve.Fields)
	}
	fields["type"] = ErrTypeChanged
	return Balance{}, &validate.ValidationError{Fields: fields}
}
