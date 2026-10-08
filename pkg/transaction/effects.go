package transaction

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
//	          to_balance_uid   value += amount  (destination: asset up / liability debt down)
type Effect struct {
	BalanceUID string
	Field      string // "balance_uid" or "to_balance_uid" (for error messages)
	Delta      int64  // signed value change in minor units of the transaction currency
}

// Effects returns the balance changes of e, in order (source first).
func (e Transaction) Effects() []Effect {
	amt := e.AmountMinor
	switch e.Type {
	case Income:
		return []Effect{{BalanceUID: e.BalanceUID, Field: "balance_uid", Delta: amt}}
	case Transfer:
		out := []Effect{{BalanceUID: e.BalanceUID, Field: "balance_uid", Delta: -amt}}
		if e.ToBalanceUID != nil {
			out = append(out, Effect{BalanceUID: *e.ToBalanceUID, Field: "to_balance_uid", Delta: amt})
		}
		return out
	default: // expense ("" is treated as expense, as elsewhere)
		return []Effect{{BalanceUID: e.BalanceUID, Field: "balance_uid", Delta: -amt}}
	}
}
