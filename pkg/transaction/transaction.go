// Package transaction holds the domain model and input validation.
package transaction

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nutmos/expensetrackr/pkg/balance"
	"github.com/nutmos/expensetrackr/pkg/category"
	"github.com/nutmos/expensetrackr/pkg/common_type"
	"github.com/nutmos/expensetrackr/pkg/helper"
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
func normalizeType(s string) Type { return Type(helper.Normalize(s)) }

// fieldError is a ValidationError for a single field.
func fieldError(field, msg string) error {
	return &ValidationError{Fields: map[string]string{field: msg}}
}

// ParseType normalizes a type a client may write; empty means Expense
// (backward compat). balance_adjustment and beginning_balance are not
// accepted (see ParseFilterType).
func ParseType(s string) (Type, bool) {
	switch t := normalizeType(s); t {
	case "":
		return Expense, true
	case Expense, Income, Transfer, AssetPurchase, ReturnOnInvestment, AssetSale, RepayDebt:
		return t, true
	}
	return "", false
}

// ParseFilterType parses a list filter type: any stored type, including
// balance_adjustment and beginning_balance. Empty is not valid here (the
// caller skips the filter).
func ParseFilterType(s string) (Type, bool) {
	switch t := normalizeType(s); t {
	case Expense, Income, Transfer, AssetPurchase, ReturnOnInvestment, AssetSale, RepayDebt, BalanceAdjustment, BeginningBalance:
		return t, true
	}
	return "", false
}

// IsPayableType reports whether a balance of this type can be used as a payment
// account on a transaction (payment accounts and credit cards only).
func IsPayableType(t balance.Type) bool {
	return t == balance.TypePaymentAccount || t == balance.TypeCreditCard
}

// SourceAllowed reports whether a balance type may be used as balance_uid for
// a transaction type:
//   - expense:  payment_account, credit_card (money is spent from it)
//   - income:   payment_account, credit_card (money received / cash rebate
//     credited back; other_asset and other_liability are not receivers)
//   - transfer: any balance type (source of the move)
//   - asset_purchase: payment_account, credit_card (money is spent from it)
//   - return_on_investment: payment_account, credit_card (the return is
//     received there; the linked other_asset is not a receiver)
//   - asset_sale: payment_account, credit_card (the proceeds are received
//     there; the sold other_asset is to_balance_uid)
//   - repay_debt: payment_account only (the money that pays the debt)
//   - balance_adjustment: any balance type (the adjusted balance)
//   - beginning_balance: any balance type (the balance it starts)
func SourceAllowed(tt Type, bt balance.Type) bool {
	if tt == Transfer || tt.ServerRecorded() {
		return bt.Valid()
	}
	if tt == RepayDebt {
		return bt == balance.TypePaymentAccount
	}
	if tt == Expense || tt == Income || tt == AssetPurchase || tt == ReturnOnInvestment || tt == AssetSale {
		return IsPayableType(bt)
	}
	return false
}

const (
	errSourceExpense       = "must refer to a payment_account or credit_card balance"
	errSourceIncome        = "must refer to a payment_account or credit_card balance (income)"
	errSourceAssetPurchase = "must refer to a payment_account or credit_card balance (asset purchase source)"
	errSourceROI           = "must refer to a payment_account or credit_card balance (return on investment)"
	errSourceAssetSale     = "must refer to a payment_account or credit_card balance (the account the sale proceeds are received in)"
	errSourceRepayDebt     = "must refer to a payment_account balance (the account the repayment is paid from)"
	errSourceGeneric       = "must refer to an existing balance"
)

func sourceRule(tt Type) string {
	if tt == Expense {
		return errSourceExpense
	}
	if tt == Income {
		return errSourceIncome
	}
	if tt == AssetPurchase {
		return errSourceAssetPurchase
	}
	if tt == ReturnOnInvestment {
		return errSourceROI
	}
	if tt == AssetSale {
		return errSourceAssetSale
	}
	if tt == RepayDebt {
		return errSourceRepayDebt
	}
	return errSourceGeneric
}

// DestinationAllowed reports whether a balance type may be used as
// to_balance_uid: any type for a transfer (e.g. paying a credit card, saving
// into an other asset, repaying a loan), only other_asset for an asset
// purchase, for the investment a return on investment is tied to and for the
// asset sold by an asset sale, and only a liability (credit_card,
// other_liability) for a debt repayment.
func DestinationAllowed(tt Type, bt balance.Type) bool {
	switch tt {
	case Transfer:
		return bt.Valid()
	case AssetPurchase, ReturnOnInvestment, AssetSale:
		return bt == balance.TypeOtherAsset
	case RepayDebt:
		return bt == balance.TypeCreditCard || bt == balance.TypeOtherLiability
	}
	return false
}

// Validate checks the input and returns a normalized Transaction (without ID,
// CreatedAt, or Account). Account is filled later by AttachPaymentBalance once
// the balance_uid has been resolved against the store. Type
// balance_adjustment and beginning_balance are rejected (see
// ValidateAdjustment and ValidateBeginningBalance).
func (in CreateInput) Validate() (Transaction, error) {
	return in.validate("")
}

// ValidateAdjustment checks a PUT body for an existing balance_adjustment:
// type must be omitted or balance_adjustment, adjustment_direction is
// required, balance_uid may name any balance, and to_balance_uid,
// category_uid, to_amount/to_currency, foreign_amount/foreign_currency and
// import_ref are not allowed.
func (in CreateInput) ValidateAdjustment() (Transaction, error) {
	if t := normalizeType(in.Type); t != "" && t != BalanceAdjustment {
		return Transaction{}, fieldError("type", ErrAdjustmentTypeChange)
	}
	in.Type = string(BalanceAdjustment)
	return in.validate(BalanceAdjustment)
}

// ValidateBeginningBalance checks a PUT body for an existing
// beginning_balance: like ValidateAdjustment, except that type must be
// omitted or beginning_balance and the amount may be 0. Whether balance_uid
// is still the beginning balance's own balance is checked by the caller,
// which knows the stored row.
func (in CreateInput) ValidateBeginningBalance() (Transaction, error) {
	if t := normalizeType(in.Type); t != "" && t != BeginningBalance {
		return Transaction{}, fieldError("type", ErrBeginningBalanceTypeChange)
	}
	in.Type = string(BeginningBalance)
	return in.validate(BeginningBalance)
}

// validate checks the input. fixed is "" for an ordinary transaction (which
// may not be a server-recorded type), or the server-recorded type of the
// transaction being edited (BalanceAdjustment, BeginningBalance).
func (in CreateInput) validate(fixed Type) (Transaction, error) {
	fields := map[string]string{}
	var exp Transaction

	cur := strings.ToUpper(strings.TrimSpace(in.Currency))
	exponent, known := money.MinorUnits(cur)
	if cur == "" {
		fields["currency"] = "is required"
	} else if !known {
		fields["currency"] = fmt.Sprintf("%q is not a recognised ISO 4217 currency code", in.Currency)
	}
	exp.Currency = cur

	if known {
		parse := money.ParseAmount
		if fixed == BeginningBalance {
			parse = money.ParseNonNegative // a balance may start at 0
		}
		minor, err := parse(string(in.Amount), exponent)
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
	if fixed != "" {
		tt, okType = fixed, true
	} else if normalizeType(in.Type) == BalanceAdjustment {
		fields["type"] = ErrAdjustmentType
	} else if normalizeType(in.Type) == BeginningBalance {
		fields["type"] = ErrBeginningBalanceType
	} else if !okType {
		fields["type"] = "must be one of: expense, income, transfer, asset_purchase, return_on_investment, asset_sale, repay_debt"
	}
	exp.Type = tt

	uid := common_type.NormalizeUID(in.BalanceUID)

	if uid == "" {
		fields["balance_uid"] = "is required"
	} else if !uid.Valid() {
		fields["balance_uid"] = "must be a UUID (the uid of a balance)"
	}
	exp.BalanceUID = uid

	// to_balance_uid is only checked once the type is known.
	to := common_type.NormalizeUID(in.ToBalanceUID)
	if !okType { // Do Nothing
	} else if !tt.HasDestination() {
		if to != "" {
			fields["to_balance_uid"] = errDestTypes
		}
	} else if to == "" {
		fields["to_balance_uid"] = "is required for " + article(tt) + " " + strings.ReplaceAll(string(tt), "_", " ")
	} else if !to.Valid() {
		fields["to_balance_uid"] = "must be a UUID (the uid of a balance)"
	} else if to == uid {
		fields["to_balance_uid"] = "must differ from balance_uid"
	} else {
		exp.ToBalanceUID = &to
	}

	// Interest and fees of a debt repayment; checked before the category,
	// which a repayment may only have when it has charges.
	if okType {
		validateCharges(in, tt, exponent, known, fields, &exp)
		validateSaleResult(in, tt, exponent, known, fields, &exp)
	}

	// A category is required for every type but transfer, repay_debt and
	// asset_sale. A transfer may not have one; a repay_debt may have an
	// expense category for its interest and fees only when it has some; an
	// asset_sale may have one for its gain or loss only when it has one. A
	// balance_adjustment or beginning_balance may not have one.
	cat := common_type.NormalizeUID(in.CategoryUID)
	if cat == "" {
		if okType && tt.NeedsCategory() {
			fields["category_uid"] = "is required for " + article(tt) + " " + strings.ReplaceAll(string(tt), "_", " ")
		}
	} else if okType && tt == RepayDebt && exp.ChargesMinor() == 0 {
		if _, bad := fields["interest_amount"]; !bad {
			if _, bad := fields["fee_amount"]; !bad {
				fields["category_uid"] = errRepayCategory
			}
		}
	} else if okType && tt == AssetSale && exp.GainLossValue() == 0 {
		if _, bad := fields["gain_loss"]; !bad {
			fields["category_uid"] = errSaleCategory
		}
	} else if okType && !tt.NeedsCategory() && tt != RepayDebt && tt != AssetSale {
		fields["category_uid"] = "is not allowed for " + article(tt) + " " + strings.ReplaceAll(string(tt), "_", " ")
	} else if !cat.Valid() {
		fields["category_uid"] = "must be a UUID (the uid of a category)"
	} else {
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

	if okType {
		validateForeign(in, tt, cur, fields, &exp)
	}

	dir := ""
	if in.AdjustmentDirection != nil {
		dir = helper.Normalize(*in.AdjustmentDirection)
	}
	if !tt.HasDirection() || !okType {
		if dir != "" {
			fields["adjustment_direction"] = "is only allowed when editing a balance_adjustment or beginning_balance"
		}
	} else if dir == "" {
		fields["adjustment_direction"] = "is required for " + article(tt) + " " + strings.ReplaceAll(string(tt), "_", " ") + " (increase or decrease)"
	} else if dir != string(Increase) && dir != string(Decrease) {
		fields["adjustment_direction"] = "must be increase or decrease"
	} else {
		d := Direction(dir)
		exp.AdjustmentDirection = &d
	}

	note := strings.TrimSpace(in.Note)
	if utf8.RuneCountInString(note) > MaxNoteLen {
		fields["note"] = fmt.Sprintf("must be at most %d characters", MaxNoteLen)
	}
	exp.Note = note

	// A nil ImportRef means none (create) / keep the stored ref (PUT).
	if in.ImportRef != nil {
		if ref := strings.TrimSpace(*in.ImportRef); ref != "" {
			if tt.ServerRecorded() {
				fields["import_ref"] = "is not allowed for " + article(tt) + " " + strings.ReplaceAll(string(tt), "_", " ")
			} else if utf8.RuneCountInString(ref) > MaxImportRefLen {
				fields["import_ref"] = fmt.Sprintf("must be at most %d characters", MaxImportRefLen)
			} else if strings.ContainsFunc(ref, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
				fields["import_ref"] = "must not contain control characters"
			} else {
				exp.ImportRef = &ref
			}
		}
	}

	if len(fields) > 0 {
		return Transaction{}, &ValidationError{Fields: fields}
	}
	return exp, nil
}

// errForeignTypes is the message for foreign_amount / foreign_currency sent
// with a type that does not allow them.
const errForeignTypes = "is only allowed when type is expense, income, asset_purchase or return_on_investment"

// validateForeign checks the optional foreign-currency amount: both fields or
// neither, a known currency other than the transaction currency, and an
// amount > 0 with at most that currency's minor digits. Problems are added to
// fields; a valid pair is set on exp.
func validateForeign(in CreateInput, tt Type, cur string, fields map[string]string, exp *Transaction) {
	rawAmt := strings.TrimSpace(string(in.ForeignAmount))
	fc := strings.ToUpper(strings.TrimSpace(in.ForeignCurrency))
	if rawAmt == "" && fc == "" {
		return
	}
	if !tt.AllowsForeignAmount() {
		if rawAmt != "" {
			fields["foreign_amount"] = errForeignTypes
		}
		if fc != "" {
			fields["foreign_currency"] = errForeignTypes
		}
		return
	}
	if fc == "" {
		fields["foreign_currency"] = "is required when foreign_amount is set"
		return
	}
	exponent, known := money.MinorUnits(fc)
	if !known {
		fields["foreign_currency"] = fmt.Sprintf("%q is not a recognised ISO 4217 currency code", in.ForeignCurrency)
	} else if fc == cur {
		fields["foreign_currency"] = "must differ from currency (omit foreign_amount and foreign_currency for a transaction in its own currency)"
	}
	if rawAmt == "" {
		fields["foreign_amount"] = "is required when foreign_currency is set"
		return
	}
	if !known {
		return
	}
	minor, err := money.ParseAmount(rawAmt, exponent)
	if err != nil {
		fields["foreign_amount"] = err.Error()
		return
	}
	if fc == cur {
		return
	}
	amt := money.FormatAmount(minor, exponent)
	exp.ForeignAmountMinor, exp.ForeignAmount, exp.ForeignCurrency = &minor, &amt, &fc
}

// errRepayCategory is the category_uid message for a repay_debt without
// interest or fees.
const errRepayCategory = "is only allowed for a repay debt with interest_amount or fee_amount (it is the expense category of those charges)"

// errChargesTypes is the message for interest_amount / fee_amount sent with a
// type other than repay_debt.
const errChargesTypes = "is only allowed when type is repay_debt"

// validateCharges checks the optional interest_amount and fee_amount of a
// debt repayment: plain decimals of 0 or more in the transaction currency
// (exponent; known is false for an unknown currency, already reported).
// Empty or 0 means none. Problems are added to fields; positive values are
// set on exp.
func validateCharges(in CreateInput, tt Type, exponent int, known bool, fields map[string]string, exp *Transaction) {
	for _, c := range []struct {
		field  string
		raw    DecimalInput
		amount **string
		minor  **int64
	}{
		{"interest_amount", in.InterestAmount, &exp.InterestAmount, &exp.InterestAmountMinor},
		{"fee_amount", in.FeeAmount, &exp.FeeAmount, &exp.FeeAmountMinor},
	} {
		raw := strings.TrimSpace(string(c.raw))
		if raw == "" {
			continue
		}
		if !tt.AllowsCharges() {
			fields[c.field] = errChargesTypes
			continue
		}
		if !known {
			continue
		}
		minor, err := money.ParseNonNegative(raw, exponent)
		if err != nil {
			fields[c.field] = err.Error()
			continue
		}
		if minor == 0 {
			continue
		}
		amt := money.FormatAmount(minor, exponent)
		*c.amount, *c.minor = &amt, &minor
	}
}

// errSaleCategory is the category_uid message for an asset_sale without a
// gain or loss.
const errSaleCategory = "is only allowed for an asset sale with a gain_loss other than 0 (it is the income category of a gain or the expense category of a loss)"

// errSaleTypes is the message for gain_loss / entire_asset_sale sent with a
// type other than asset_sale.
const errSaleTypes = "is only allowed when type is asset_sale"

// validateSaleResult checks the optional gain_loss and entire_asset_sale of
// an asset sale: gain_loss is a signed plain decimal in the transaction
// currency (exponent; known is false for an unknown currency, already
// reported), empty or 0 meaning neither gain nor loss, and the proceeds
// (amount + gain_loss) may not be negative: a loss is at most the principal.
// Problems are added to fields; valid values are set on exp.
func validateSaleResult(in CreateInput, tt Type, exponent int, known bool, fields map[string]string, exp *Transaction) {
	raw := strings.TrimSpace(string(in.GainLoss))
	if !tt.AllowsSaleResult() {
		if raw != "" {
			fields["gain_loss"] = errSaleTypes
		}
		if in.EntireAssetSale {
			fields["entire_asset_sale"] = errSaleTypes
		}
		return
	}
	exp.EntireAssetSale = in.EntireAssetSale
	if raw == "" || !known {
		return
	}
	minor, err := money.ParseSigned(raw, exponent)
	if err != nil {
		fields["gain_loss"] = err.Error()
		return
	}
	if minor == 0 {
		return
	}
	if _, bad := fields["amount"]; !bad && minor < 0 && -minor > exp.AmountMinor {
		fields["gain_loss"] = "a loss may not be larger than the principal (amount): the account cannot receive less than 0"
		return
	}
	if minor > 0 && exp.AmountMinor > math.MaxInt64-minor {
		fields["gain_loss"] = "amount + gain_loss is out of range"
		return
	}
	amt := money.FormatAmount(minor, exponent)
	exp.GainLoss, exp.GainLossMinor = &amt, &minor
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

const (
	errDestAssetPurchase = "must refer to an other_asset balance (asset purchase destination)"
	errDestROI           = "must refer to an other_asset balance (the investment this return is tied to)"
	errDestAssetSale     = "must refer to an other_asset balance (the asset being sold)"
	errDestRepayDebt     = "must refer to a credit_card or other_liability balance (the debt being repaid)"
	errDestGeneric       = "must refer to an existing balance"
)

// destinationRule is the to_balance_uid message when DestinationAllowed fails.
func destinationRule(tt Type) string {
	if tt == AssetPurchase {
		return errDestAssetPurchase
	}
	if tt == ReturnOnInvestment {
		return errDestROI
	}
	if tt == AssetSale {
		return errDestAssetSale
	}
	if tt == RepayDebt {
		return errDestRepayDebt
	}
	return errDestGeneric
}

// errDestTypes is the to_balance_uid message for a type without a destination.
const errDestTypes = "is only allowed when type is transfer, asset_purchase, return_on_investment, asset_sale or repay_debt"

// AttachDestinationBalance checks that b is allowed as to_balance_uid for
// e.Type (see DestinationAllowed) and sets the destination snapshot.
func (e *Transaction) AttachDestinationBalance(b balance.Balance) error {
	if !e.Type.HasDestination() || e.ToBalanceUID == nil {
		return fieldError("to_balance_uid", errDestTypes)
	}
	if b.UID == "" || !e.ToBalanceUID.Matches(b.UID.String()) {
		return fieldError("to_balance_uid", "does not match any balance")
	}
	if !DestinationAllowed(e.Type, b.Type) {
		return fieldError("to_balance_uid", destinationRule(e.Type))
	}
	uid, name := b.UID, b.Name
	e.ToBalanceUID, e.ToAccount = &uid, &name
	return nil
}

// NeedsCategory reports whether a new or edited transaction of this type must
// have a category: every type except transfer, repay_debt, asset_sale (whose
// category is optional), balance_adjustment and beginning_balance (which may
// not have one). Transactions saved before categories were required may still
// have none.
func (t Type) NeedsCategory() bool {
	return t != Transfer && t != RepayDebt && t != AssetSale && !t.ServerRecorded()
}

// article is "an" before the vowel-initial type names, else "a".
func article(t Type) string {
	if strings.ContainsRune("aeiou", rune(string(t)[0])) {
		return "an"
	}
	return "a"
}

// categoryType is the category type a transaction needs: expense for asset
// purchases and for the charges of a debt repayment, income for returns on
// investment, income for the gain and expense for the loss of an asset sale,
// else the type itself.
func (e Transaction) categoryType() string {
	switch e.Type {
	case AssetPurchase, RepayDebt:
		return string(Expense)
	case ReturnOnInvestment:
		return string(Income)
	case AssetSale:
		if e.GainLossValue() < 0 {
			return string(Expense)
		}
		return string(Income)
	}
	return string(e.Type)
}

// AttachCategory checks that c is the referenced category and matches the
// transaction type (expense categories for expenses, asset purchases, debt
// repayment charges and asset sale losses, income categories for income,
// returns on investment and asset sale gains). Only the uid is stored on the transaction;
// the name is looked up from the categories table (or GET /api/categories)
// when needed.
func (e *Transaction) AttachCategory(c category.Category) error {
	if e.CategoryUID == nil || c.UID == "" || !e.CategoryUID.Matches(c.UID.String()) {
		return fieldError("category_uid", "does not match any category")
	}
	want := e.categoryType()
	if string(c.Type) != want {
		return fieldError("category_uid", fmt.Sprintf("must be an %s category (this one is %s)", want, c.Type))
	}
	uid := c.UID
	e.CategoryUID = &uid
	return nil
}
