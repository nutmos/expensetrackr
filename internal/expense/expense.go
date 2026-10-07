// Package expense holds the domain model and input validation.
package expense

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"expense-service/internal/money"
	"expense-service/internal/validate"
)

const (
	MaxAccountLen = 100
	MaxNoteLen    = 1000
)

// Expense is a stored expense record.
type Expense struct {
	ID          int64     `json:"id"`
	Amount      string    `json:"amount"`       // decimal string, e.g. "120.50"
	AmountMinor int64     `json:"amount_minor"` // integer minor units, e.g. 12050
	Currency    string    `json:"currency"`     // ISO 4217, e.g. "THB"
	Account     string    `json:"account"`      // payment account, e.g. "KBank debit"
	SpentAt     string    `json:"spent_at"`     // RFC 3339 with the offset as entered
	Note        string    `json:"note"`
	CreatedAt   string    `json:"created_at"` // RFC 3339, UTC
	UpdatedAt   *string   `json:"updated_at"` // RFC 3339, UTC; null until edited
	SpentTime   time.Time `json:"-"`
}

// DecimalInput keeps the literal text of a JSON string or number amount.
type DecimalInput = money.DecimalInput

// ValidationError and RequestError are shared with other domain packages.
type (
	ValidationError = validate.ValidationError
	RequestError    = validate.RequestError
)

// CreateInput is the payload accepted by POST /api/expenses and
// PUT /api/expenses/:id (full replace).
type CreateInput struct {
	Amount   DecimalInput `json:"amount"`
	Currency string       `json:"currency"`
	Account  string       `json:"account"`
	SpentAt  string       `json:"spent_at"`
	Note     string       `json:"note"`
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

// Validate checks the input and returns a normalized Expense (without ID or
// CreatedAt), or a *ValidationError listing every invalid field.
func (in CreateInput) Validate() (Expense, error) {
	fields := map[string]string{}
	var exp Expense

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

	acct := strings.TrimSpace(in.Account)
	switch {
	case acct == "":
		fields["account"] = "is required"
	case utf8.RuneCountInString(acct) > MaxAccountLen:
		fields["account"] = fmt.Sprintf("must be at most %d characters", MaxAccountLen)
	}
	exp.Account = acct

	if t, err := ParseTimestamp(in.SpentAt); err != nil {
		fields["spent_at"] = err.Error()
	} else {
		exp.SpentTime = t
		// Normalize to RFC 3339 (second precision) while preserving the offset.
		exp.SpentAt = t.Truncate(time.Second).Format(time.RFC3339)
	}

	note := strings.TrimSpace(in.Note)
	if utf8.RuneCountInString(note) > MaxNoteLen {
		fields["note"] = fmt.Sprintf("must be at most %d characters", MaxNoteLen)
	}
	exp.Note = note

	if len(fields) > 0 {
		return Expense{}, &ValidationError{Fields: fields}
	}
	return exp, nil
}
