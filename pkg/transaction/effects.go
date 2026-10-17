package transaction

import "github.com/nutmos/expensetrackr/pkg/common_type"

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
	BalanceUID common_type.UID
	Field      string // "balance_uid" or "to_balance_uid" (for error messages)
	Delta      int64  // signed value change in minor units of Currency
	Currency   string // currency of Delta ("" = the transaction currency)
}

// DestinationCurrency is the currency the destination balance moves in:
// to_currency for a cross-currency transfer, else the transaction currency.
func (e Transaction) DestinationCurrency() string {
	if e.ToCurrency != nil {
		return *e.ToCurrency
	}
	return e.Currency
}

// Effects returns the balance changes of e, in order (source first).
func (e Transaction) Effects() []Effect {
	amt := e.AmountMinor
	switch e.Type {
	case BalanceAdjustment:
		return nil
	case Income:
		return []Effect{{BalanceUID: e.BalanceUID, Field: "balance_uid", Delta: amt}}
	case Transfer, AssetPurchase:
		out := []Effect{{BalanceUID: e.BalanceUID, Field: "balance_uid", Delta: -amt}}
		if e.ToBalanceUID != nil {
			to := amt
			if e.ToAmountMinor != nil {
				to = *e.ToAmountMinor // cross-currency transfer: in to_currency
			}
			out = append(out, Effect{BalanceUID: *e.ToBalanceUID, Field: "to_balance_uid", Delta: to, Currency: e.DestinationCurrency()})
		}
		return out
	default: // expense ("" is treated as expense, as elsewhere)
		return []Effect{{BalanceUID: e.BalanceUID, Field: "balance_uid", Delta: -amt}}
	}
}
