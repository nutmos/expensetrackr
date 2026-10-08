// Package transaction holds the domain model and input validation.
package transaction

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"expense-service/internal/balance"
	"expense-service/internal/category"
	"expense-service/internal/money"
	"expense-service/internal/validate"
)

const (
	MaxNoteLen = 1000
	uidLen     = 36 // UUID v4 canonical form
)

// Transaction is a stored transaction record.
type Transaction struct {
	ID           int64     `json:"-"`              // internal row id; the API identifies transactions by UID
	UID          string    `json:"uid"`            // server-assigned UUID v4, immutable
	Amount       string    `json:"amount"`         // decimal string, e.g. "120.50"
	AmountMinor  int64     `json:"amount_minor"`   // integer minor units, e.g. 12050
	Currency     string    `json:"currency"`       // ISO 4217, e.g. "THB"
	Type         Type      `json:"type"`           // expense | income | transfer
	BalanceUID   string    `json:"balance_uid"`    // paying (expense), receiving (income) or source (transfer) balance
	Account      string    `json:"account"`        // denormalized balance name at write time
	ToBalanceUID *string   `json:"to_balance_uid"` // transfer destination; null otherwise
	ToAccount    *string   `json:"to_account"`     // denormalized destination name; null otherwise
	CategoryUID  *string   `json:"category_uid"`   // optional link to categories.uid; null for none / transfers
	SpentAt      string    `json:"spent_at"`       // RFC 3339 with the offset as entered
	Note         string    `json:"note"`
	CreatedAt    string    `json:"created_at"` // RFC 3339, UTC
	UpdatedAt    *string   `json:"updated_at"` // RFC 3339, UTC; null until edited
	SpentTime    time.Time `json:"-"`
}

// DecimalInput keeps the literal text of a JSON string or number amount.
type DecimalInput = money.DecimalInput

// ValidationError and RequestError are shared with other domain packages.
type (
	ValidationError = validate.ValidationError
	RequestError    = validate.RequestError
)

// CreateInput is the payload accepted by POST /api/transactions and
// PUT /api/transactions/:id (full replace). Payment account is identified by
// balance_uid (not free text). The read-only "account" name is filled in by
// the API from the balance after validation.
type CreateInput struct {
	Amount       DecimalInput `json:"amount"`
	Currency     string       `json:"currency"`
	Type         string       `json:"type"` // optional on create/PUT; defaults to "expense"
	BalanceUID   string       `json:"balance_uid"`
	ToBalanceUID string       `json:"to_balance_uid"` // transfer only
	CategoryUID  string       `json:"category_uid"`   // optional; expense/income only
	SpentAt      string       `json:"spent_at"`
	Note         string       `json:"note"`

	// IgnoredUID accepts a "uid" in PUT bodies so a previous response can be
	// round-tripped; it is never used (the uid is server-assigned, immutable).
	IgnoredUID string `json:"uid,omitempty"`
}

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

// looksLikeUID reports whether s has the canonical UUID shape (version digit
// not checked here; the balances table is the source of truth).
func looksLikeUID(s string) bool {
	if len(s) != uidLen {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return false
			}
		}
	}
	return true
}

// Type is the direction of a transaction. The amount is always positive.
type Type string

const (
	Expense  Type = "expense"
	Income   Type = "income"
	Transfer Type = "transfer"
)

// ParseType normalizes a type string; empty means Expense (backward compat).
func ParseType(s string) (Type, bool) {
	switch t := Type(strings.ToLower(strings.TrimSpace(s))); t {
	case "":
		return Expense, true
	case Expense, Income, Transfer:
		return t, true
	}
	return "", false
}

// SourceAllowed reports whether a balance type may be used as balance_uid for
// a transaction type:
//   - expense:  payment_account, credit_card (money is spent from it)
//   - income:   payment_account, other_asset (money is received into it)
//   - transfer: any balance type (source of the move)
func SourceAllowed(tt Type, bt balance.Type) bool {
	switch tt {
	case Expense:
		return bt == balance.PaymentAccount || bt == balance.CreditCard
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
	}
	return "must refer to an existing balance"
}

// IsPayableType reports whether a balance of this type can be used as a payment
// account on a transaction (payment accounts and credit cards only).
func IsPayableType(t balance.Type) bool {
	return t == balance.PaymentAccount || t == balance.CreditCard
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
	if !okType {
		fields["type"] = "must be one of: expense, income, transfer"
	}
	exp.Type = tt

	uid := strings.ToLower(strings.TrimSpace(in.BalanceUID))
	switch {
	case uid == "":
		fields["balance_uid"] = "is required"
	case !looksLikeUID(uid):
		fields["balance_uid"] = "must be a UUID (the uid of a balance)"
	}
	exp.BalanceUID = uid

	to := strings.ToLower(strings.TrimSpace(in.ToBalanceUID))
	if okType {
		switch {
		case tt != Transfer && to != "":
			fields["to_balance_uid"] = "is only allowed when type is transfer"
		case tt == Transfer && to == "":
			fields["to_balance_uid"] = "is required for a transfer"
		case tt == Transfer && !looksLikeUID(to):
			fields["to_balance_uid"] = "must be a UUID (the uid of a balance)"
		case tt == Transfer && to == uid:
			fields["to_balance_uid"] = "must differ from balance_uid"
		}
	}
	if tt == Transfer && to != "" {
		exp.ToBalanceUID = &to
	}

	cat := strings.ToLower(strings.TrimSpace(in.CategoryUID))
	switch {
	case cat == "":
	case okType && tt == Transfer:
		fields["category_uid"] = "is not allowed for a transfer"
	case !looksLikeUID(cat):
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

	note := strings.TrimSpace(in.Note)
	if utf8.RuneCountInString(note) > MaxNoteLen {
		fields["note"] = fmt.Sprintf("must be at most %d characters", MaxNoteLen)
	}
	exp.Note = note

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
		return &ValidationError{Fields: map[string]string{
			"balance_uid": sourceRule(tt),
		}}
	}
	if e.BalanceUID != "" && !strings.EqualFold(e.BalanceUID, b.UID) {
		return &ValidationError{Fields: map[string]string{
			"balance_uid": "does not match the resolved balance",
		}}
	}
	e.BalanceUID = b.UID
	e.Account = b.Name
	return nil
}

// AttachDestinationBalance sets the transfer destination snapshot. Any
// balance type is accepted (e.g. paying a credit card, saving into an other
// asset, repaying a loan).
func (e *Transaction) AttachDestinationBalance(b balance.Balance) error {
	if e.Type != Transfer || e.ToBalanceUID == nil {
		return &ValidationError{Fields: map[string]string{"to_balance_uid": "is only allowed when type is transfer"}}
	}
	if b.UID == "" || !strings.EqualFold(*e.ToBalanceUID, b.UID) {
		return &ValidationError{Fields: map[string]string{"to_balance_uid": "does not match any balance"}}
	}
	uid, name := b.UID, b.Name
	e.ToBalanceUID, e.ToAccount = &uid, &name
	return nil
}

// AttachCategory checks that c is the referenced category and matches the
// transaction type (expense categories for expenses, income categories for
// income). Only the uid is stored on the transaction; the name is looked up
// from the categories table (or GET /api/categories) when needed.
func (e *Transaction) AttachCategory(c category.Category) error {
	if e.CategoryUID == nil || c.UID == "" || !strings.EqualFold(*e.CategoryUID, c.UID) {
		return &ValidationError{Fields: map[string]string{"category_uid": "does not match any category"}}
	}
	if string(c.Type) != string(e.Type) {
		return &ValidationError{Fields: map[string]string{
			"category_uid": fmt.Sprintf("must be an %s category (this one is %s)", e.Type, c.Type),
		}}
	}
	uid := c.UID
	e.CategoryUID = &uid
	return nil
}
