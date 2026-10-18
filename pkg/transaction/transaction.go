// Package transaction holds the domain model and input validation.
package transaction

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nutmos/expensetrackr/pkg/balance"
	"github.com/nutmos/expensetrackr/pkg/category"
	"github.com/nutmos/expensetrackr/pkg/common_type"
	"github.com/nutmos/expensetrackr/pkg/money"
)

// ParseTimestamp parses a strict RFC 3339 / ISO 8601 date-time that carries an
// explicit UTC offset ("Z" or "+08:00"). Values without an offset are rejected.
func ParseTimestamp(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, errors.New("is required")
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, errors.New("must be an RFC 3339 / ISO 8601 date-time with a timezone offset, e.g. 2026-10-06T21:06:00+08:00")
	}
	return t, nil
}

// normalizeType trims and lower-cases a type as sent by a client.
func normalizeType(s string) Type { return Type(strings.ToLower(strings.TrimSpace(s))) }

// fieldError is a ValidationError for a single field.
func fieldError(field, msg string) error {
	return &ValidationError{Fields: map[string]string{field: msg}}
}

// ParseType normalizes a type a client may write; empty means Expense
// (backward compat). balance_adjustment is not accepted (see ParseFilterType).
func ParseType(s string) (Type, bool) {
	switch t := normalizeType(s); t {
	case "":
		return Expense, true
	case Expense, Income, Transfer, AssetPurchase:
		return t, true
	}
	return "", false
}

// ParseFilterType parses a list filter type: any stored type, including
// balance_adjustment. Empty is not valid here (the caller skips the filter).
func ParseFilterType(s string) (Type, bool) {
	switch t := normalizeType(s); t {
	case Expense, Income, Transfer, AssetPurchase, BalanceAdjustment:
		return t, true
	}
	return "", false
}

// IsPayableType reports whether a balance of this type can be used as a payment
// account on a transaction (payment accounts and credit cards only).
func IsPayableType(t balance.Type) bool {
	return t == balance.PaymentAccount || t == balance.CreditCard
}

// SourceAllowed reports whether a balance type may be used as balance_uid for
// a transaction type:
//   - expense:  payment_account, credit_card (money is spent from it)
//   - income:   payment_account, other_asset (money is received into it)
//   - transfer: any balance type (source of the move)
//   - asset_purchase: payment_account, credit_card (money is spent from it)
func SourceAllowed(tt Type, bt balance.Type) bool {
	switch tt {
	case Expense, AssetPurchase:
		return IsPayableType(bt)
	case Income:
		return bt == balance.PaymentAccount || bt == balance.OtherAsset
	case Transfer:
		return bt.Valid()
	}
	return false
}

func sourceRule(tt Type) string {
	switch tt {
	case Expense:
		return "must refer to a payment_account or credit_card balance"
	case Income:
		return "must refer to a payment_account or other_asset balance (income)"
	case AssetPurchase:
		return "must refer to a payment_account or credit_card balance (asset purchase source)"
	}
	return "must refer to an existing balance"
}

// DestinationAllowed reports whether a balance type may be used as
// to_balance_uid: any type for a transfer (e.g. paying a credit card, saving
// into an other asset, repaying a loan), only other_asset for an asset
// purchase.
func DestinationAllowed(tt Type, bt balance.Type) bool {
	switch tt {
	case Transfer:
		return bt.Valid()
	case AssetPurchase:
		return bt == balance.OtherAsset
	}
	return false
}

// Validate checks the input and returns a normalized Transaction (without ID,
// CreatedAt, or Account). Account is filled later by AttachPaymentBalance once
// the balance_uid has been resolved against the store.
func (in CreateInput) Validate() (Transaction, error) {
	fields := map[string]string{}
	var exp Transaction

	cur := strings.ToUpper(strings.TrimSpace(in.Currency))
	exponent, known := money.MinorUnits(cur)
	switch {
	case cur == "":
		fields["currency"] = "is required"
	case !known:
		fields["currency"] = fmt.Sprintf("%q is not a recognised ISO 4217 currency code", in.Currency)
	}
	exp.Currency = cur

	if known {
		minor, err := money.ParseAmount(string(in.Amount), exponent)
		if err != nil {
			fields["amount"] = err.Error()
		} else {
			exp.AmountMinor = minor
			exp.Amount = money.FormatAmount(minor, exponent)
		}
	} else if strings.TrimSpace(string(in.Amount)) == "" {
		fields["amount"] = "is required"
	}

	tt, okType := ParseType(in.Type)
	switch {
	case okType:
	case normalizeType(in.Type) == BalanceAdjustment:
		fields["type"] = ErrAdjustmentType
	default:
		fields["type"] = "must be one of: expense, income, transfer, asset_purchase"
	}
	exp.Type = tt

	uid := common_type.NormalizeUID(in.BalanceUID)
	switch {
	case uid == "":
		fields["balance_uid"] = "is required"
	case !uid.Valid():
		fields["balance_uid"] = "must be a UUID (the uid of a balance)"
	}
	exp.BalanceUID = uid

	// to_balance_uid is only checked once the type is known.
	to := common_type.NormalizeUID(in.ToBalanceUID)
	switch {
	case !okType:
	case !tt.HasDestination():
		if to != "" {
			fields["to_balance_uid"] = "is only allowed when type is transfer or asset_purchase"
		}
	case to == "":
		fields["to_balance_uid"] = "is required for a " + strings.ReplaceAll(string(tt), "_", " ")
	case !to.Valid():
		fields["to_balance_uid"] = "must be a UUID (the uid of a balance)"
	case to == uid:
		fields["to_balance_uid"] = "must differ from balance_uid"
	default:
		exp.ToBalanceUID = &to
	}

	cat := common_type.NormalizeUID(in.CategoryUID)
	switch {
	case cat == "":
	case okType && tt == Transfer:
		fields["category_uid"] = "is not allowed for a transfer"
	case !cat.Valid():
		fields["category_uid"] = "must be a UUID (the uid of a category)"
	default:
		exp.CategoryUID = &cat
	}

	if t, err := ParseTimestamp(in.SpentAt); err != nil {
		fields["spent_at"] = err.Error()
	} else {
		exp.SpentTime = t
		exp.SpentAt = t.Truncate(time.Second).Format(time.RFC3339)
	}

	if okType && tt != Transfer {
		if strings.TrimSpace(string(in.ToAmount)) != "" {
			fields["to_amount"] = "is only allowed when type is transfer"
		}
		if strings.TrimSpace(in.ToCurrency) != "" {
			fields["to_currency"] = "is only allowed when type is transfer"
		}
	}

	note := strings.TrimSpace(in.Note)
	if utf8.RuneCountInString(note) > MaxNoteLen {
		fields["note"] = fmt.Sprintf("must be at most %d characters", MaxNoteLen)
	}
	exp.Note = note

	// A nil ImportRef means none (create) / keep the stored ref (PUT).
	if in.ImportRef != nil {
		if ref := strings.TrimSpace(*in.ImportRef); ref != "" {
			switch {
			case utf8.RuneCountInString(ref) > MaxImportRefLen:
				fields["import_ref"] = fmt.Sprintf("must be at most %d characters", MaxImportRefLen)
			case strings.ContainsFunc(ref, func(r rune) bool { return r < 0x20 || r == 0x7f }):
				fields["import_ref"] = "must not contain control characters"
			default:
				exp.ImportRef = &ref
			}
		}
	}

	if len(fields) > 0 {
		return Transaction{}, &ValidationError{Fields: fields}
	}
	return exp, nil
}

// AttachPaymentBalance checks that b is allowed as balance_uid for e.Type and
// copies its name into Account as a denormalized snapshot. Call after
// Validate, once the uid has been loaded from the store.
func (e *Transaction) AttachPaymentBalance(b balance.Balance) error {
	tt := e.Type
	if tt == "" {
		tt = Expense
	}
	if b.UID == "" || !SourceAllowed(tt, b.Type) {
		return fieldError("balance_uid", sourceRule(tt))
	}
	if e.BalanceUID != "" && !e.BalanceUID.Matches(b.UID.String()) {
		return fieldError("balance_uid", "does not match the resolved balance")
	}
	e.BalanceUID = b.UID
	e.Account = b.Name
	return nil
}

// AttachDestinationBalance checks that b is allowed as to_balance_uid for
// e.Type (see DestinationAllowed) and sets the destination snapshot.
func (e *Transaction) AttachDestinationBalance(b balance.Balance) error {
	if !e.Type.HasDestination() || e.ToBalanceUID == nil {
		return fieldError("to_balance_uid", "is only allowed when type is transfer or asset_purchase")
	}
	if b.UID == "" || !e.ToBalanceUID.Matches(b.UID.String()) {
		return fieldError("to_balance_uid", "does not match any balance")
	}
	if !DestinationAllowed(e.Type, b.Type) {
		return fieldError("to_balance_uid", "must refer to an other_asset balance (asset purchase destination)")
	}
	uid, name := b.UID, b.Name
	e.ToBalanceUID, e.ToAccount = &uid, &name
	return nil
}

// AttachCategory checks that c is the referenced category and matches the
// transaction type (expense categories for expenses and asset purchases,
// income categories for income). Only the uid is stored on the transaction;
// the name is looked up from the categories table (or GET /api/categories)
// when needed.
func (e *Transaction) AttachCategory(c category.Category) error {
	if e.CategoryUID == nil || c.UID == "" || !e.CategoryUID.Matches(c.UID.String()) {
		return fieldError("category_uid", "does not match any category")
	}
	want := string(e.Type)
	if e.Type == AssetPurchase {
		want = string(Expense)
	}
	if string(c.Type) != want {
		return fieldError("category_uid", fmt.Sprintf("must be an %s category (this one is %s)", want, c.Type))
	}
	uid := c.UID
	e.CategoryUID = &uid
	return nil
}
