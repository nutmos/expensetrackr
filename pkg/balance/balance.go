// Package balance models account balances: payment accounts and other assets
// (which have a balance) and credit cards and other liabilities (which have a
// debt and a limit). Amounts are integer minor units, like expenses.
package balance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/nutmos/expensetrackr/pkg/money"
	"github.com/nutmos/expensetrackr/pkg/validate"
)

// Type is the kind of balance.
type Type string

const (
	PaymentAccount Type = "payment_account"
	CreditCard     Type = "credit_card"
	OtherAsset     Type = "other_asset"
	OtherLiability Type = "other_liability"
)

// Types lists every type in display order.
var Types = []Type{PaymentAccount, CreditCard, OtherAsset, OtherLiability}

// Valid reports whether t is a known type.
func (t Type) Valid() bool { return t.IsAsset() || t.IsLiability() }

// IsAsset reports whether t carries a balance (payment_account, other_asset).
func (t Type) IsAsset() bool { return t == PaymentAccount || t == OtherAsset }

// IsLiability reports whether t carries a debt and a limit (credit_card, other_liability).
func (t Type) IsLiability() bool { return t == CreditCard || t == OtherLiability }

// Kind is "asset" or "liability" ("" for unknown types).
func (t Type) Kind() string {
	switch {
	case t.IsAsset():
		return "asset"
	case t.IsLiability():
		return "liability"
	}
	return ""
}

func typeList() string {
	s := make([]string, len(Types))
	for i, t := range Types {
		s[i] = string(t)
	}
	return strings.Join(s, ", ")
}

const (
	MaxNameLen        = 100 // same as an expense's payment account, so names can match later
	MaxDescriptionLen = 1000
)

// Balance is a stored balance record. Fields that do not apply to the type are
// null in JSON: assets have balance; liabilities have debt, limit, available
// and over_limit.
type Balance struct {
	ID             int64   `json:"-"`   // internal row id; the API identifies balances by UID
	UID            string  `json:"uid"` // server-assigned UUID v4; immutable
	Name           string  `json:"name"`
	Type           Type    `json:"type"`
	Kind           string  `json:"kind"`     // "asset" or "liability" (derived from type)
	Currency       string  `json:"currency"` // base currency, ISO 4217
	Description    string  `json:"description"`
	Balance        *string `json:"balance"` // decimal string; may be negative (overdraft)
	BalanceMinor   *int64  `json:"balance_minor"`
	Debt           *string `json:"debt"` // may be negative (overpaid / in credit)
	DebtMinor      *int64  `json:"debt_minor"`
	Limit          *string `json:"limit"` // >= 0
	LimitMinor     *int64  `json:"limit_minor"`
	Available      *string `json:"available"` // limit - debt; negative when over the limit
	AvailableMinor *int64  `json:"available_minor"`
	OverLimit      *bool   `json:"over_limit"` // debt > limit
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      *string `json:"updated_at"`
	// Version starts at 1 and is incremented on every change, manual (PUT /
	// PATCH) or automatic (a transaction moving the balance). PUT/PATCH must
	// send the version they last read (If-Match or "version").
	Version int64 `json:"version"`
	Scale   int   `json:"-"` // minor-unit digits used for the *_minor values
}

// SetAmounts fills the amount fields and the derived ones from minor units.
// Pass nil for amounts that do not apply to the type.
func (b *Balance) SetAmounts(scale int, bal, debt, limit *int64) {
	b.Scale = scale
	b.Kind = b.Type.Kind()
	b.Balance, b.BalanceMinor = nil, nil
	b.Debt, b.DebtMinor, b.Limit, b.LimitMinor = nil, nil, nil, nil
	b.Available, b.AvailableMinor, b.OverLimit = nil, nil, nil
	format := func(v int64) *string { s := money.FormatAmount(v, scale); return &s }
	if bal != nil {
		v := *bal
		b.BalanceMinor, b.Balance = &v, format(v)
	}
	if debt != nil {
		v := *debt
		b.DebtMinor, b.Debt = &v, format(v)
	}
	if limit != nil {
		v := *limit
		b.LimitMinor, b.Limit = &v, format(v)
	}
	if debt != nil && limit != nil {
		avail := *limit - *debt
		over := *debt > *limit
		b.AvailableMinor, b.Available, b.OverLimit = &avail, format(avail), &over
	}
}

// Input is the payload for POST and PUT /api/balances (and the merge target
// for PATCH). An amount that is absent, null or "" counts as not provided.
// UID is accepted so clients can round-trip a previous response; it is
// ignored (the server assigns and never changes it).
type Input struct {
	UID         string              `json:"uid,omitempty"` // ignored; server-assigned
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
	// AdjustmentNote is an optional reason recorded in balance_adjustments
	// when a PUT/PATCH changes balance, debt or limit. Not stored on the balance.
	AdjustmentNote string `json:"adjustment_note,omitempty"`
}

func provided(d *money.DecimalInput) bool {
	return d != nil && strings.TrimSpace(string(*d)) != ""
}

// Validate checks the input and returns a normalized Balance (without ID and
// timestamps), or a *validate.ValidationError listing every invalid field.
func (in Input) Validate() (Balance, error) {
	fields := map[string]string{}
	var b Balance

	name := strings.TrimSpace(in.Name)
	switch {
	case name == "":
		fields["name"] = "is required"
	case utf8.RuneCountInString(name) > MaxNameLen:
		fields["name"] = fmt.Sprintf("must be at most %d characters", MaxNameLen)
	}
	b.Name = name

	t := Type(strings.ToLower(strings.TrimSpace(in.Type)))
	switch {
	case t == "":
		fields["type"] = "is required (one of " + typeList() + ")"
	case !t.Valid():
		fields["type"] = fmt.Sprintf("%q is not valid; use one of %s", in.Type, typeList())
	}
	b.Type = t

	cur := strings.ToUpper(strings.TrimSpace(in.Currency))
	scale, known := money.MinorUnits(cur)
	switch {
	case cur == "":
		fields["currency"] = "is required"
	case !known:
		fields["currency"] = fmt.Sprintf("%q is not a recognised ISO 4217 currency code", in.Currency)
	}
	b.Currency = cur

	desc := strings.TrimSpace(in.Description)
	if utf8.RuneCountInString(desc) > MaxDescriptionLen {
		fields["description"] = fmt.Sprintf("must be at most %d characters", MaxDescriptionLen)
	}
	b.Description = desc

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
	switch {
	case t.IsAsset():
		bal = parse("balance", in.Balance, true, money.ParseSigned)
		notUsed("debt", in.Debt, "credit_card and other_liability")
		notUsed("limit", in.Limit, "credit_card and other_liability")
	case t.IsLiability():
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

// ErrTypeChanged is the field message for an update that tries to change type.
const ErrTypeChanged = "balance type cannot be changed after creation"

// ValidateUpdate validates in as the new state of a stored balance of type
// stored (PUT, and PATCH after ApplyPatch). The type is fixed at creation: in
// must carry the same type (case/space-insensitive); a different one is a
// validation error on "type". The other fields are then still checked against
// the stored type, so every problem is reported at once.
func (in Input) ValidateUpdate(stored Type) (Balance, error) {
	t := Type(strings.ToLower(strings.TrimSpace(in.Type)))
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

// Input returns the editable fields of a stored balance, for PATCH merging.
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

var patchFields = map[string]bool{
	"name": true, "type": true, "currency": true, "description": true,
	"balance": true, "debt": true, "limit": true,
}

func isNull(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

// ApplyPatch overlays the fields present in a JSON-object patch onto in.
// Absent fields are unchanged. "description": null clears the description;
// "balance"/"debt"/"limit": null removes that amount. null for name, type or
// currency is a validation error. "type" may be sent but must equal the stored
// type; the caller must run ValidateUpdate (which enforces that) on the result.
func (in *Input) ApplyPatch(patch map[string]json.RawMessage) error {
	// uid is immutable; drop it so round-tripping a previous response is fine.
	// "version" and "adjustment_note" are request metadata, read by the API
	// before this point, not fields of the balance.
	delete(patch, "uid")
	delete(patch, "version")
	delete(patch, "adjustment_note")
	if len(patch) == 0 {
		return &validate.RequestError{Msg: "patch must contain at least one of: name, type, currency, description, balance, debt, limit"}
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

	nullFields := map[string]string{}
	for _, f := range []struct {
		name string
		dst  *string
	}{{"name", &in.Name}, {"type", &in.Type}, {"currency", &in.Currency}, {"description", &in.Description}} {
		raw, ok := patch[f.name]
		if !ok {
			continue
		}
		if isNull(raw) {
			if f.name == "description" {
				*f.dst = ""
			} else {
				nullFields[f.name] = "cannot be null"
			}
			continue
		}
		if err := json.Unmarshal(raw, f.dst); err != nil {
			return &validate.RequestError{Msg: fmt.Sprintf("field %q must be a string", f.name)}
		}
	}
	for _, f := range []struct {
		name string
		dst  **money.DecimalInput
	}{{"balance", &in.Balance}, {"debt", &in.Debt}, {"limit", &in.Limit}} {
		raw, ok := patch[f.name]
		if !ok {
			continue
		}
		if isNull(raw) {
			*f.dst = nil
			continue
		}
		var d money.DecimalInput
		if err := json.Unmarshal(raw, &d); err != nil {
			return &validate.RequestError{Msg: fmt.Sprintf("field %q must be a decimal string or number", f.name)}
		}
		*f.dst = &d
	}
	if len(nullFields) > 0 {
		return &validate.ValidationError{Fields: nullFields}
	}
	return nil
}

// ErrOverflow is returned by ApplyValueDelta when the result does not fit.
var ErrOverflow = errors.New("amount out of range")

// ApplyValueDelta changes the balance's value by delta minor units (same
// currency and scale as the balance). Value is the balance of an asset and
// minus the debt of a liability, so a positive delta raises an asset's
// balance or lowers a liability's debt (possibly below zero: in credit). The
// derived fields (available, over_limit) are recomputed.
func (b *Balance) ApplyValueDelta(delta int64) error {
	switch {
	case b.Type.IsAsset() && b.BalanceMinor != nil:
		v, ok := addChecked(*b.BalanceMinor, delta)
		if !ok {
			return ErrOverflow
		}
		b.SetAmounts(b.Scale, &v, nil, nil)
	case b.Type.IsLiability() && b.DebtMinor != nil && b.LimitMinor != nil:
		if delta == math.MinInt64 {
			return ErrOverflow
		}
		v, ok := addChecked(*b.DebtMinor, -delta)
		if !ok {
			return ErrOverflow
		}
		limit := *b.LimitMinor
		b.SetAmounts(b.Scale, nil, &v, &limit)
	default:
		return fmt.Errorf("balance %q has no amount for type %q", b.UID, b.Type)
	}
	return nil
}

func addChecked(a, b int64) (int64, bool) {
	c := a + b
	if (b > 0 && c < a) || (b < 0 && c > a) {
		return 0, false
	}
	return c, true
}

// MaxAdjustmentNoteLen bounds the optional reason of a manual adjustment.
const MaxAdjustmentNoteLen = 500

// Adjustment is one manual change of a balance amount (balance, debt or
// limit) made through PUT/PATCH, kept as an audit trail in the
// balance_adjustments table. Changes made by transactions are not recorded
// here (the transactions themselves are the record).
type Adjustment struct {
	ID          int64   `json:"-"`
	UID         string  `json:"uid"`
	BalanceUID  string  `json:"balance_uid"`
	Field       string  `json:"field"`  // balance | debt | limit
	Old         string  `json:"old"`    // decimal string in OldCurrency
	New         string  `json:"new"`    // decimal string in Currency
	Change      *string `json:"change"` // New - Old; null if the currency changed too
	Currency    string  `json:"currency"`
	OldCurrency string  `json:"old_currency"`
	Note        string  `json:"note"`
	Version     int64   `json:"version"` // the balance version this change created
	CreatedAt   string  `json:"created_at"`
}

// Totals aggregates balances of one currency.
type Totals struct {
	Currency        string `json:"currency"`
	Assets          string `json:"assets"`           // sum of balances (asset types)
	Liabilities     string `json:"liabilities"`      // sum of debts (liability types)
	Net             string `json:"net"`              // assets - liabilities
	CreditLimit     string `json:"credit_limit"`     // sum of limits (liability types)
	AvailableCredit string `json:"available_credit"` // credit_limit - liabilities
}

// ComputeTotals sums per currency, exactly in minor units. Currencies are
// returned in alphabetical order. No exchange-rate conversion is done.
func ComputeTotals(items []Balance) []Totals {
	type acc struct {
		scale               int
		assets, debt, limit int64
	}
	sums := map[string]*acc{}
	for _, b := range items {
		a := sums[b.Currency]
		if a == nil {
			a = &acc{scale: b.Scale}
			sums[b.Currency] = a
		}
		if b.BalanceMinor != nil {
			a.assets += *b.BalanceMinor
		}
		if b.DebtMinor != nil {
			a.debt += *b.DebtMinor
		}
		if b.LimitMinor != nil {
			a.limit += *b.LimitMinor
		}
	}
	out := make([]Totals, 0, len(sums))
	for cur, a := range sums {
		f := func(v int64) string { return money.FormatAmount(v, a.scale) }
		out = append(out, Totals{
			Currency: cur, Assets: f(a.assets), Liabilities: f(a.debt), Net: f(a.assets - a.debt),
			CreditLimit: f(a.limit), AvailableCredit: f(a.limit - a.debt),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Currency < out[j].Currency })
	return out
}
