package transaction

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
	case BalanceAdjustment, BeginningBalance:
		if e.AdjustmentDirection != nil && *e.AdjustmentDirection == Decrease {
			amt = -amt
		}
		return []Effect{{BalanceUID: e.BalanceUID, Field: "balance_uid", Delta: amt, ByAmount: true}}
	case Income, ReturnOnInvestment: // ROI: the linked asset is not moved
		return []Effect{{BalanceUID: e.BalanceUID, Field: "balance_uid", Delta: amt}}
	case AssetSale:
		// The account receives the proceeds (principal + gain, or principal
		// - loss); the sold asset goes down by the principal only.
		out := []Effect{{BalanceUID: e.BalanceUID, Field: "balance_uid", Delta: amt + e.GainLossValue()}}
		if e.ToBalanceUID != nil {
			out = append(out, Effect{BalanceUID: *e.ToBalanceUID, Field: "to_balance_uid", Delta: -amt})
		}
		return out
	case Transfer, AssetPurchase, RepayDebt:
		// repay_debt: the account also pays the interest and fees; only the
		// principal (amount) reduces the debt.
		out := []Effect{{BalanceUID: e.BalanceUID, Field: "balance_uid", Delta: -(amt + e.ChargesMinor())}}
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
