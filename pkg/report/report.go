// Package report builds the read-only summaries behind GET /api/reports/...
// (issue #136) from stored transactions and balances. It does no I/O: the
// store supplies aggregated rows and balances, this package shapes them.
//
// Every amount is exact (integer minor units summed with math/big) and
// reported per currency; nothing is converted between currencies.
//
// To add a report type: add an entry to Catalog, a Build function here, a
// handler in pkg/api/reports.go, the path in pkg/apidoc/openapi.yaml and a
// view in pkg/web/static/reports.js.
package report

import (
	"math/big"
	"sort"

	"github.com/nutmos/expensetrackr/pkg/balance"
	"github.com/nutmos/expensetrackr/pkg/common_type"
	"github.com/nutmos/expensetrackr/pkg/money"
	"github.com/nutmos/expensetrackr/pkg/transaction"
)

// Report ids, used in /api/reports/<id> and the web page's /reports/<id>.
const (
	IncomeExpenseID       = "income-expense"
	AssetsLiabilitiesID   = "assets-liabilities"
	BalanceTransactionsID = "balance-transactions"
)

// Info describes one available report (GET /api/reports).
type Info struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Path        string `json:"path"` // API path of the report
}

// Catalog lists every report type, in display order.
var Catalog = []Info{
	{
		ID:          IncomeExpenseID,
		Name:        "Income & expenses",
		Description: "Income and expenses in a time range, per currency and by category.",
		Path:        "/api/reports/" + IncomeExpenseID,
	},
	{
		ID:          AssetsLiabilitiesID,
		Name:        "Assets & liabilities",
		Description: "Current assets and liabilities, per currency, by balance type and by account.",
		Path:        "/api/reports/" + AssetsLiabilitiesID,
	},
	{
		ID:          BalanceTransactionsID,
		Name:        "Balance transactions",
		Description: "Every transaction of one balance in a time range, with money in and out and the running amount (a statement).",
		Path:        "/api/reports/" + BalanceTransactionsID,
	},
}

// Known reports whether id is a report in Catalog.
func Known(id string) bool {
	for _, r := range Catalog {
		if r.ID == id {
			return true
		}
	}
	return false
}

// ---- Income & expenses ------------------------------------------------------

// FlowRow is one aggregated group of transactions from the store: the sum and
// count of transactions sharing type, currency, category and stored scale.
type FlowRow struct {
	Type        transaction.Type
	Currency    string
	CategoryUID *common_type.UID // nil = no category (older rows)
	Scale       int              // amount_scale the rows were stored with
	SumMinor    *big.Int
	Count       int64
}

// FlowTypes are the transaction types counted by the income & expense
// report: expenses as expense; income and returns on investment as income.
// Transfers, asset purchases, debt repayments and balance adjustments move
// value between balances (or correct them) and are left out, like the
// totals under the transaction list; only the interest and fees of a debt
// repayment are counted, as expense, and the gain (income) or loss (expense)
// of an asset sale (the store reports them as Income / Expense rows).
var FlowTypes = []transaction.Type{transaction.Expense, transaction.Income, transaction.ReturnOnInvestment}

// CategoryAmount is the total of one category within one currency.
type CategoryAmount struct {
	CategoryUID  *common_type.UID `json:"category_uid"`  // null = transactions saved without a category
	CategoryName *string          `json:"category_name"` // null when there is no category (or it no longer exists)
	Amount       string           `json:"amount"`        // decimal string
	AmountMinor  string           `json:"amount_minor"`  // integer minor units, as a string (sums may exceed int64)
	Count        int64            `json:"count"`         // number of transactions
}

// CurrencyFlow is income and expenses in one currency.
type CurrencyFlow struct {
	Currency          string           `json:"currency"`
	Income            string           `json:"income"`
	Expense           string           `json:"expense"`
	Net               string           `json:"net"` // income - expense
	IncomeCount       int64            `json:"income_count"`
	ExpenseCount      int64            `json:"expense_count"`
	IncomeByCategory  []CategoryAmount `json:"income_by_category"`  // largest first
	ExpenseByCategory []CategoryAmount `json:"expense_by_category"` // largest first
}

// IncomeExpense is the body of GET /api/reports/income-expense.
type IncomeExpense struct {
	Report     string         `json:"report"` // "income-expense"
	From       *string        `json:"from"`   // the from filter as sent (RFC 3339); null = no lower bound
	To         *string        `json:"to"`     // the to filter as sent (RFC 3339); null = no upper bound
	Currencies []CurrencyFlow `json:"currencies"`
}

// BuildIncomeExpense groups rows per currency (alphabetical) and per
// category (largest total first, then name). names maps category uids to
// names; a uid not in it gets a null name. Rows of other types are ignored.
func BuildIncomeExpense(rows []FlowRow, names map[common_type.UID]string) []CurrencyFlow {
	type catKey struct{ uid common_type.UID } // "" = no category
	type catAcc struct {
		sum   big.Int
		count int64
	}
	type side struct {
		total big.Int
		count int64
		cats  map[catKey]*catAcc
	}
	type acc struct {
		scale           int
		income, expense side
	}
	sums := map[string]*acc{}
	for _, r := range rows {
		var isIncome bool
		switch r.Type {
		case transaction.Expense:
		case transaction.Income, transaction.ReturnOnInvestment:
			isIncome = true
		default:
			continue
		}
		a := sums[r.Currency]
		if a == nil {
			scale, ok := money.MinorUnits(r.Currency)
			if !ok {
				scale = r.Scale
			}
			a = &acc{scale: scale}
			a.income.cats, a.expense.cats = map[catKey]*catAcc{}, map[catKey]*catAcc{}
			sums[r.Currency] = a
		}
		s := &a.expense
		if isIncome {
			s = &a.income
		}
		v := money.RescaleMinor(r.SumMinor, r.Scale, a.scale)
		s.total.Add(&s.total, v)
		s.count += r.Count
		var k catKey
		if r.CategoryUID != nil {
			k.uid = *r.CategoryUID
		}
		c := s.cats[k]
		if c == nil {
			c = &catAcc{}
			s.cats[k] = c
		}
		c.sum.Add(&c.sum, v)
		c.count += r.Count
	}

	out := make([]CurrencyFlow, 0, len(sums))
	for cur, a := range sums {
		f := func(v *big.Int) string { return money.FormatBigAmount(v, a.scale) }
		byCat := func(s *side) []CategoryAmount {
			type item struct {
				ca  CategoryAmount
				sum *big.Int
			}
			items := make([]item, 0, len(s.cats))
			for k, c := range s.cats {
				ca := CategoryAmount{Amount: f(&c.sum), AmountMinor: c.sum.String(), Count: c.count}
				if k.uid != "" {
					uid := k.uid
					ca.CategoryUID = &uid
					if n, ok := names[uid]; ok {
						ca.CategoryName = &n
					}
				}
				items = append(items, item{ca, &c.sum})
			}
			sort.Slice(items, func(i, j int) bool {
				if c := items[i].sum.Cmp(items[j].sum); c != 0 {
					return c > 0
				}
				return categorySortKey(items[i].ca) < categorySortKey(items[j].ca)
			})
			res := make([]CategoryAmount, len(items))
			for i, it := range items {
				res[i] = it.ca
			}
			return res
		}
		out = append(out, CurrencyFlow{
			Currency:          cur,
			Income:            f(&a.income.total),
			Expense:           f(&a.expense.total),
			Net:               f(new(big.Int).Sub(&a.income.total, &a.expense.total)),
			IncomeCount:       a.income.count,
			ExpenseCount:      a.expense.count,
			IncomeByCategory:  byCat(&a.income),
			ExpenseByCategory: byCat(&a.expense),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Currency < out[j].Currency })
	return out
}

// categorySortKey orders equal totals: named categories by name, then
// unknown ones by uid, then "no category" last.
func categorySortKey(c CategoryAmount) string {
	switch {
	case c.CategoryName != nil:
		return "0" + *c.CategoryName
	case c.CategoryUID != nil:
		return "1" + string(*c.CategoryUID)
	default:
		return "2"
	}
}

// ---- Assets & liabilities ---------------------------------------------------

// AccountAmount is one balance in the assets & liabilities report. Amount is
// the balance of an asset or the debt of a liability (either may be
// negative: an overdrawn account, a card in credit).
type AccountAmount struct {
	UID         common_type.UID `json:"uid"`
	Name        string          `json:"name"`
	Type        balance.Type    `json:"type"`
	Kind        balance.Kind    `json:"kind"`
	Amount      string          `json:"amount"`
	AmountMinor int64           `json:"amount_minor"`
}

// TypeAmount is the total of one balance type within one currency.
type TypeAmount struct {
	Type   balance.Type `json:"type"`
	Kind   balance.Kind `json:"kind"`
	Amount string       `json:"amount"` // sum of balances (assets) or debts (liabilities)
	Count  int          `json:"count"`  // number of balances
}

// CurrencyPosition is the assets and liabilities held in one currency.
type CurrencyPosition struct {
	Currency    string          `json:"currency"`
	Assets      string          `json:"assets"`      // sum of asset balances
	Liabilities string          `json:"liabilities"` // sum of liability debts
	Net         string          `json:"net"`         // assets - liabilities
	ByType      []TypeAmount    `json:"by_type"`     // balance types present, in balance.Types order
	Accounts    []AccountAmount `json:"accounts"`    // assets then liabilities, each largest first
}

// AssetsLiabilities is the body of GET /api/reports/assets-liabilities.
type AssetsLiabilities struct {
	Report     string             `json:"report"` // "assets-liabilities"
	AsOf       string             `json:"as_of"`  // server time of the report (RFC 3339, UTC)
	Currencies []CurrencyPosition `json:"currencies"`
}

// BuildAssetsLiabilities groups balances per currency (alphabetical).
func BuildAssetsLiabilities(items []balance.Balance) []CurrencyPosition {
	type typeAcc struct {
		sum   big.Int
		count int
	}
	type acc struct {
		scale         int
		assets, debts big.Int
		types         map[balance.Type]*typeAcc
		accounts      []AccountAmount
	}
	sums := map[string]*acc{}
	for _, b := range items {
		var v *int64
		switch {
		case b.Type.IsAsset():
			v = b.BalanceMinor
		case b.Type.IsLiability():
			v = b.DebtMinor
		}
		if v == nil {
			continue
		}
		a := sums[b.Currency]
		if a == nil {
			a = &acc{scale: b.Scale, types: map[balance.Type]*typeAcc{}}
			sums[b.Currency] = a
		}
		bv := big.NewInt(*v)
		if b.Type.IsAsset() {
			a.assets.Add(&a.assets, bv)
		} else {
			a.debts.Add(&a.debts, bv)
		}
		t := a.types[b.Type]
		if t == nil {
			t = &typeAcc{}
			a.types[b.Type] = t
		}
		t.sum.Add(&t.sum, bv)
		t.count++
		a.accounts = append(a.accounts, AccountAmount{
			UID: b.UID, Name: b.Name, Type: b.Type, Kind: b.Type.Kind(),
			Amount: money.FormatAmount(*v, b.Scale), AmountMinor: *v,
		})
	}

	out := make([]CurrencyPosition, 0, len(sums))
	for cur, a := range sums {
		f := func(v *big.Int) string { return money.FormatBigAmount(v, a.scale) }
		byType := []TypeAmount{}
		for _, t := range balance.Types {
			if ta := a.types[t]; ta != nil {
				byType = append(byType, TypeAmount{Type: t, Kind: t.Kind(), Amount: f(&ta.sum), Count: ta.count})
			}
		}
		sort.SliceStable(a.accounts, func(i, j int) bool {
			x, y := a.accounts[i], a.accounts[j]
			if x.Kind != y.Kind {
				return x.Kind == balance.KindAsset
			}
			if x.AmountMinor != y.AmountMinor {
				return x.AmountMinor > y.AmountMinor
			}
			return x.Name < y.Name
		})
		out = append(out, CurrencyPosition{
			Currency:    cur,
			Assets:      f(&a.assets),
			Liabilities: f(&a.debts),
			Net:         f(new(big.Int).Sub(&a.assets, &a.debts)),
			ByType:      byType,
			Accounts:    a.accounts,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Currency < out[j].Currency })
	return out
}
