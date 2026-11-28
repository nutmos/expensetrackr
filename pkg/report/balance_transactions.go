package report

import (
	"math/big"
	"sort"

	"github.com/nutmos/expensetrackr/pkg/balance"
	"github.com/nutmos/expensetrackr/pkg/common_type"
	"github.com/nutmos/expensetrackr/pkg/money"
	"github.com/nutmos/expensetrackr/pkg/transaction"
)

// ---- Balance transactions ---------------------------------------------------
//
// A statement of one balance (issue #160): every transaction that moved it in
// a time range, oldest first, with money in / out, the amount after each one
// and the opening and closing amounts. The web page can print it to PDF.

// BalanceRef identifies the balance a statement is for.
type BalanceRef struct {
	UID      common_type.UID `json:"uid"`
	Name     string          `json:"name"`
	Type     balance.Type    `json:"type"`
	Kind     balance.Kind    `json:"kind"`
	Currency string          `json:"currency"`
	Hidden   bool            `json:"hidden"`
	Amount   string          `json:"amount"` // current balance (asset) or debt (liability)
}

// BalanceEntry is one transaction in a balance statement. MoneyIn / MoneyOut
// are what the transaction did to the balance's value, in the balance's
// currency: for an asset, in = deposits and out = spending; for a liability,
// in = repayments (the debt goes down) and out = charges (the debt goes up).
// Exactly one of them is non-null.
type BalanceEntry struct {
	UID          common_type.UID  `json:"uid"`
	Type         transaction.Type `json:"type"`
	SpentAt      string           `json:"spent_at"`
	CategoryUID  *common_type.UID `json:"category_uid"`
	CategoryName *string          `json:"category_name"` // null without a category (or when it no longer exists)
	Counterparty *string          `json:"counterparty"`  // the other balance of a transfer, asset purchase, return on investment or debt repayment
	Note         string           `json:"note"`
	Amount       string           `json:"amount"`   // the transaction's amount as recorded
	Currency     string           `json:"currency"` // the transaction's currency
	MoneyIn      *string          `json:"money_in"`
	MoneyOut     *string          `json:"money_out"`
	AmountAfter  string           `json:"amount_after"` // balance (asset) or debt (liability) right after this transaction
}

// BalanceTransactions is the body of GET /api/reports/balance-transactions.
type BalanceTransactions struct {
	Report        string         `json:"report"` // "balance-transactions"
	Balance       BalanceRef     `json:"balance"`
	From          *string        `json:"from"` // the from filter as sent (RFC 3339); null = no lower bound
	To            *string        `json:"to"`   // the to filter as sent (RFC 3339); null = no upper bound
	OpeningAmount string         `json:"opening_amount"`
	ClosingAmount string         `json:"closing_amount"`
	MoneyIn       string         `json:"money_in"`  // sum of money_in
	MoneyOut      string         `json:"money_out"` // sum of money_out
	Net           string         `json:"net"`       // money_in - money_out (change in value)
	Count         int            `json:"count"`
	Transactions  []BalanceEntry `json:"transactions"` // oldest first
}

// valueDelta is the change tx made to the value of b (positive = more value:
// more balance for an asset, less debt for a liability), in b's minor units.
// A transaction that does not touch b has no effect.
func valueDelta(b balance.Balance, tx transaction.Transaction) *big.Int {
	d := new(big.Int)
	for _, ef := range tx.Effects() {
		if ef.BalanceUID == b.UID {
			d.Add(d, big.NewInt(ef.ValueDelta(b.Type.IsLiability())))
		}
	}
	return d
}

// BuildBalanceTransactions builds the statement of b from inRange (the
// transactions that use b, spent in the range) and after (those spent after
// the range, used to work back from b's current amount to the closing
// amount). Both may be in any order; entries are sorted oldest first (by
// spend time, a beginning balance first, then by input order). names maps category uids to names.
func BuildBalanceTransactions(b balance.Balance, inRange, after []transaction.Transaction, names map[common_type.UID]string) BalanceTransactions {
	liability := b.Type.IsLiability()
	scale := b.Scale
	f := func(v *big.Int) string { return money.FormatBigAmount(v, scale) }
	// shown turns a value into the amount the user sees: the balance of an
	// asset, the debt of a liability.
	shown := func(v *big.Int) string {
		if liability {
			return f(new(big.Int).Neg(v))
		}
		return f(v)
	}

	var current big.Int
	if b.BalanceMinor != nil {
		current.SetInt64(*b.BalanceMinor)
	} else if b.DebtMinor != nil {
		current.SetInt64(-*b.DebtMinor)
	}

	// closing = current - every change made after the range.
	closing := new(big.Int).Set(&current)
	for _, tx := range after {
		closing.Sub(closing, valueDelta(b, tx))
	}

	txs := append([]transaction.Transaction(nil), inRange...)
	// The beginning balance comes first among transactions at the same time:
	// it is moved to the time of the earliest one recorded before it.
	sort.SliceStable(txs, func(i, j int) bool {
		if !txs[i].SpentTime.Equal(txs[j].SpentTime) {
			return txs[i].SpentTime.Before(txs[j].SpentTime)
		}
		return txs[i].Type == transaction.BeginningBalance && txs[j].Type != transaction.BeginningBalance
	})

	deltas := make([]*big.Int, len(txs))
	var in, out big.Int
	for i, tx := range txs {
		d := valueDelta(b, tx)
		deltas[i] = d
		if d.Sign() >= 0 {
			in.Add(&in, d)
		} else {
			out.Sub(&out, d)
		}
	}
	net := new(big.Int).Sub(&in, &out)
	opening := new(big.Int).Sub(closing, net)

	entries := make([]BalanceEntry, len(txs))
	running := new(big.Int).Set(opening)
	for i, tx := range txs {
		d := deltas[i]
		running.Add(running, d)
		e := BalanceEntry{
			UID: tx.UID, Type: tx.Type, SpentAt: tx.SpentAt, Note: tx.Note,
			Amount: tx.Amount, Currency: tx.Currency, AmountAfter: shown(running),
		}
		if tx.CategoryUID != nil {
			uid := *tx.CategoryUID
			e.CategoryUID = &uid
			if n, ok := names[uid]; ok {
				e.CategoryName = &n
			}
		}
		if tx.BalanceUID == b.UID && tx.ToAccount != nil {
			n := *tx.ToAccount
			e.Counterparty = &n
		} else if tx.BalanceUID != b.UID {
			n := tx.Account
			e.Counterparty = &n
		}
		if d.Sign() >= 0 {
			v := f(d)
			e.MoneyIn = &v
		} else {
			v := f(new(big.Int).Neg(d))
			e.MoneyOut = &v
		}
		entries[i] = e
	}

	return BalanceTransactions{
		Report: BalanceTransactionsID,
		Balance: BalanceRef{
			UID: b.UID, Name: b.Name, Type: b.Type, Kind: b.Type.Kind(),
			Currency: b.Currency, Hidden: b.Hidden, Amount: shown(&current),
		},
		OpeningAmount: shown(opening),
		ClosingAmount: shown(closing),
		MoneyIn:       f(&in),
		MoneyOut:      f(&out),
		Net:           f(net),
		Count:         len(entries),
		Transactions:  entries,
	}
}
