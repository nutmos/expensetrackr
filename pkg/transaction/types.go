package transaction

import (
	"time"

	"github.com/nutmos/expensetrackr/pkg/money"
	"github.com/nutmos/expensetrackr/pkg/validate"
)

const (
	MaxNoteLen      = 1000
	MaxImportRefLen = 200 // characters
	uidLen          = 36  // canonical UUID form
)

// Transaction is a stored transaction record.
type Transaction struct {
	ID           int64   `json:"-"`              // internal row id; the API identifies transactions by UID
	UID          string  `json:"uid"`            // server-assigned UUID v4, immutable
	Amount       string  `json:"amount"`         // decimal string, e.g. "120.50"
	AmountMinor  int64   `json:"amount_minor"`   // integer minor units, e.g. 12050
	Currency     string  `json:"currency"`       // ISO 4217, e.g. "THB"
	Type         Type    `json:"type"`           // expense | income | transfer | asset_purchase | balance_adjustment
	BalanceUID   string  `json:"balance_uid"`    // paying (expense, asset_purchase), receiving (income) or source (transfer) balance
	Account      string  `json:"account"`        // denormalized balance name at write time
	ToBalanceUID *string `json:"to_balance_uid"` // transfer / asset_purchase destination; null otherwise
	ToAccount    *string `json:"to_account"`     // denormalized destination name; null otherwise
	CategoryUID  *string `json:"category_uid"`   // optional link to categories.uid; null for none / transfers
	SpentAt      string  `json:"spent_at"`       // RFC 3339 with the offset as entered
	Note         string  `json:"note"`
	CreatedAt    string  `json:"created_at"` // RFC 3339, UTC
	UpdatedAt    *string `json:"updated_at"` // RFC 3339, UTC; null until edited
	// Version starts at 1 and is incremented on every change. PUT/PATCH must
	// send the version they last read (If-Match or "version"); see docs/balances.md.
	Version int64 `json:"version"`
	// AdjustsBalances is true when this transaction moved its balances (every
	// transaction created by schema v11+). Rows recorded before v11 never did,
	// so editing or deleting them leaves balances alone.
	AdjustsBalances bool `json:"adjusts_balances"`
	// AdjustmentDirection is set only for type balance_adjustment: whether the
	// manual edit increased or decreased the edited amount (balance for an
	// asset, debt for a liability). Amount is always the positive difference.
	AdjustmentDirection *Direction `json:"adjustment_direction"`
	// Cross-currency transfers only (null otherwise): the amount credited to
	// the destination in its own currency, the rate used (destination units
	// per 1 source unit) and where the rate came from. See docs/exchange-rates.md.
	ToAmount      *string `json:"to_amount"`       // decimal string in to_currency
	ToAmountMinor *int64  `json:"to_amount_minor"` // to_amount in minor units of to_currency
	ToCurrency    *string `json:"to_currency"`     // destination balance currency
	ExchangeRate  *string `json:"exchange_rate"`   // decimal string, to_currency per 1 currency
	RateSource    *string `json:"rate_source"`     // "frankfurter" | "manual"
	RateDate      *string `json:"rate_date"`       // YYYY-MM-DD of the Frankfurter rate; null for manual
	// ImportRef is an optional client-chosen idempotency key (e.g. a bank
	// reference, or a hash of date+amount+description made by an importing
	// agent). Unique per balance_uid; null when not set. See docs/bulk-import.md.
	ImportRef *string `json:"import_ref"`
	// ConversionRates is response-only and never stored: for a cross-currency
	// transfer, both directions of the effective rate (to_amount / amount and
	// amount / to_amount), computed by MarshalJSON on every encode; null
	// otherwise. Clients must not send it (unknown field, 400). See
	// ComputeConversionRates and docs/exchange-rates.md.
	ConversionRates []ConversionRate `json:"conversion_rates"`
	SpentTime       time.Time        `json:"-"`
}

// CreateInput is the payload accepted by POST /api/transactions and
// PUT /api/transactions/:id (full replace). Payment account is identified by
// balance_uid (not free text). The read-only "account" name is filled in by
// the API from the balance after validation.
type CreateInput struct {
	Amount       DecimalInput `json:"amount"`
	Currency     string       `json:"currency"`
	Type         string       `json:"type"` // optional on create/PUT; defaults to "expense"
	BalanceUID   string       `json:"balance_uid"`
	ToBalanceUID string       `json:"to_balance_uid"` // transfer / asset_purchase only
	CategoryUID  string       `json:"category_uid"`   // optional; not allowed for a transfer
	SpentAt      string       `json:"spent_at"`
	Note         string       `json:"note"`
	// ToAmount (transfer only) is the amount received by the destination, in
	// its currency. Required to match amount for a same-currency transfer;
	// for a cross-currency transfer it sets a manual rate, and when omitted the
	// server converts with the Frankfurter rate of the spent_at date.
	ToAmount DecimalInput `json:"to_amount"`
	// ToCurrency is optional; if sent it must equal the destination balance currency.
	ToCurrency string `json:"to_currency"`
	// ImportRef is an optional idempotency key, at most MaxImportRefLen
	// characters, unique per balance_uid. On create, nil or "" means none.
	// On PUT, nil (omitted or null) keeps the stored ref and "" clears it.
	ImportRef *string `json:"import_ref"`

	// IgnoredUID accepts a "uid" in PUT bodies so a previous response can be
	// round-tripped; it is never used (the uid is server-assigned, immutable).
	IgnoredUID string `json:"uid,omitempty"`
	// Version is the optimistic-locking version for PUT (alternative to the
	// If-Match header). Ignored on create.
	Version *int64 `json:"version,omitempty"`
}

// Direction is the sign of a balance adjustment.
type Direction string

const (
	Increase Direction = "increase"
	Decrease Direction = "decrease"
)

// DecimalInput keeps the literal text of a JSON string or number amount.
type DecimalInput = money.DecimalInput

// ValidationError and RequestError are shared with other domain packages.
type (
	ValidationError = validate.ValidationError
	RequestError    = validate.RequestError
)

// Type is the direction of a transaction. The amount is always positive.
type Type string

const (
	Expense  Type = "expense"
	Income   Type = "income"
	Transfer Type = "transfer"
	// AssetPurchase ("Asset Purchase") moves money from a payment account or
	// credit card (balance_uid, affected like an expense) into an other_asset
	// balance (to_balance_uid, value up). It may carry an optional expense
	// category; it is not counted as an expense or income.
	AssetPurchase Type = "asset_purchase"
	// BalanceAdjustment ("Balance Adjustment") is recorded automatically when
	// a balance's amount is edited by hand (PUT/PATCH /api/balances/:uid). It
	// cannot be created, edited or deleted through the transactions API and
	// never moves a balance itself (the manual edit already set the value).
	BalanceAdjustment Type = "balance_adjustment"
)

// HasDestination reports whether the type uses to_balance_uid (transfer and
// asset_purchase).
func (t Type) HasDestination() bool { return t == Transfer || t == AssetPurchase }

// ErrAdjustmentType is the field message for an attempt to use type
// balance_adjustment in POST/PUT/PATCH /api/transactions.
const ErrAdjustmentType = "balance_adjustment transactions are created automatically when a balance amount is edited (PUT/PATCH /api/balances/:uid); they cannot be created or changed here"

// ConversionRate is one direction of the conversion a cross-currency
// transfer actually made: Rate units of To per 1 From. It has the same
// from/to/rate shape as GET /api/exchange-rates.
type ConversionRate struct {
	From string `json:"from"` // ISO 4217
	To   string `json:"to"`   // ISO 4217
	Rate string `json:"rate"` // decimal string, up to fx.RateSigDigits significant digits
}

// Effect is the change one transaction makes to one balance, as a signed
// change in the balance's *value*: for an asset (payment_account,
// other_asset) value is its balance; for a liability (credit_card,
// other_liability) value is minus its debt. So a positive delta raises an
// asset's balance or lowers a liability's debt, and a negative delta lowers an
// asset's balance or raises a liability's debt.
//
// Sign rules (amount is always > 0):
//
//	expense   balance_uid      value -= amount  (asset balance down / card debt up)
//	income    balance_uid      value += amount  (asset balance up)
//	transfer  balance_uid      value -= amount  (source: asset down / liability debt up)
//	          to_balance_uid   value += amount  (destination: asset up / liability debt down;
//	                           to_amount in to_currency for a cross-currency transfer)
//	asset_purchase balance_uid value -= amount  (source account down / card debt up, like an expense)
//	          to_balance_uid   value += amount  (destination other_asset up)
//	balance_adjustment         none             (records a manual edit that already set the value)
type Effect struct {
	BalanceUID string
	Field      string // "balance_uid" or "to_balance_uid" (for error messages)
	Delta      int64  // signed value change in minor units of Currency
	Currency   string // currency of Delta ("" = the transaction currency)
}
