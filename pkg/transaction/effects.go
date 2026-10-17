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
