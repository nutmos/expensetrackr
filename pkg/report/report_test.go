package report

import (
	"math/big"
	"testing"

	"github.com/nutmos/expensetrackr/pkg/balance"
	"github.com/nutmos/expensetrackr/pkg/common_type"
	"github.com/nutmos/expensetrackr/pkg/transaction"
)

func uid(s string) *common_type.UID { u := common_type.UID(s); return &u }

func TestBuildIncomeExpense(t *testing.T) {
	big1, _ := new(big.Int).SetString("9223372036854775807", 10) // max int64
	rows := []FlowRow{
		{Type: transaction.Expense, Currency: "THB", CategoryUID: uid("a"), Scale: 2, SumMinor: big.NewInt(1050), Count: 2},
		{Type: transaction.Expense, Currency: "THB", CategoryUID: nil, Scale: 2, SumMinor: big.NewInt(1050), Count: 1},
		{Type: transaction.Expense, Currency: "THB", CategoryUID: uid("b"), Scale: 2, SumMinor: big.NewInt(5000), Count: 1},
		{Type: transaction.Income, Currency: "THB", CategoryUID: uid("s"), Scale: 2, SumMinor: big.NewInt(100000), Count: 1},
		// A return on investment in the same income category merges with income.
		{Type: transaction.ReturnOnInvestment, Currency: "THB", CategoryUID: uid("s"), Scale: 2, SumMinor: big.NewInt(500), Count: 1},
		// Stored with an older, smaller scale: rescaled to KWD's 3 digits.
		{Type: transaction.Income, Currency: "KWD", CategoryUID: uid("gone"), Scale: 2, SumMinor: big.NewInt(150), Count: 1},
		// Sums beyond int64 stay exact.
		{Type: transaction.Expense, Currency: "USD", Scale: 2, SumMinor: big1, Count: 1},
		{Type: transaction.Expense, Currency: "USD", Scale: 2, SumMinor: big1, Count: 1},
		{Type: transaction.Transfer, Currency: "THB", Scale: 2, SumMinor: big.NewInt(999), Count: 1}, // ignored
	}
	names := map[common_type.UID]string{"a": "Food", "b": "Travel", "s": "Salary"}
	got := BuildIncomeExpense(rows, names)
	if len(got) != 3 || got[0].Currency != "KWD" || got[1].Currency != "THB" || got[2].Currency != "USD" {
		t.Fatalf("currencies: %+v", got)
	}
	kwd, thb, usd := got[0], got[1], got[2]
	if kwd.Income != "1.500" || kwd.Expense != "0.000" || kwd.IncomeByCategory[0].CategoryName != nil || *kwd.IncomeByCategory[0].CategoryUID != "gone" {
		t.Errorf("KWD: %+v", kwd)
	}
	if thb.Expense != "71.00" || thb.Income != "1005.00" || thb.Net != "934.00" || thb.ExpenseCount != 4 || thb.IncomeCount != 2 {
		t.Errorf("THB: %+v", thb)
	}
	exp := thb.ExpenseByCategory
	if len(exp) != 3 || *exp[0].CategoryName != "Travel" || *exp[1].CategoryName != "Food" || exp[2].CategoryUID != nil || exp[2].CategoryName != nil {
		t.Errorf("THB expense order: %+v", exp)
	}
	if len(thb.IncomeByCategory) != 1 || thb.IncomeByCategory[0].Amount != "1005.00" || thb.IncomeByCategory[0].Count != 2 {
		t.Errorf("THB income: %+v", thb.IncomeByCategory)
	}
	if usd.Expense != "184467440737095516.14" || usd.ExpenseByCategory[0].AmountMinor != "18446744073709551614" || usd.Net != "-184467440737095516.14" {
		t.Errorf("USD: %+v", usd)
	}
	if out := BuildIncomeExpense(nil, nil); out == nil || len(out) != 0 {
		t.Errorf("empty: %#v", out)
	}
}

func bal(name string, typ balance.Type, cur string, scale int, v int64) balance.Balance {
	b := balance.Balance{UID: common_type.UID(name), Name: name, Type: typ, Currency: cur}
	if typ.IsAsset() {
		b.SetAmounts(scale, &v, nil, nil)
	} else {
		limit := int64(0)
		b.SetAmounts(scale, nil, &v, &limit)
	}
	return b
}

func TestBuildAssetsLiabilities(t *testing.T) {
	got := BuildAssetsLiabilities([]balance.Balance{
		bal("Loan", balance.TypeOtherLiability, "THB", 2, 100000),
		bal("Wallet", balance.TypePaymentAccount, "THB", 2, 5000),
		bal("Card", balance.TypeCreditCard, "THB", 2, -200), // in credit
		bal("Gold", balance.TypeOtherAsset, "THB", 2, 90000),
		bal("Yen", balance.TypePaymentAccount, "JPY", 0, 1000),
	})
	if len(got) != 2 || got[0].Currency != "JPY" || got[1].Currency != "THB" {
		t.Fatalf("currencies: %+v", got)
	}
	thb := got[1]
	if thb.Assets != "950.00" || thb.Liabilities != "998.00" || thb.Net != "-48.00" {
		t.Errorf("THB totals: %+v", thb)
	}
	var order string
	for _, a := range thb.Accounts {
		order += a.Name + ","
	}
	if order != "Gold,Wallet,Loan,Card," {
		t.Errorf("order: %s", order)
	}
	if len(thb.ByType) != 4 || thb.ByType[0].Type != balance.TypePaymentAccount || thb.ByType[1].Type != balance.TypeCreditCard ||
		thb.ByType[1].Amount != "-2.00" || thb.ByType[1].Kind != balance.KindLiability {
		t.Errorf("by type: %+v", thb.ByType)
	}
	if jpy := got[0]; jpy.Assets != "1000" || jpy.Liabilities != "0" || len(jpy.ByType) != 1 {
		t.Errorf("JPY: %+v", jpy)
	}
}

func TestCatalog(t *testing.T) {
	if !Known(IncomeExpenseID) || !Known(AssetsLiabilitiesID) || Known("nope") {
		t.Error("Known")
	}
	for _, r := range Catalog {
		if r.Path != "/api/reports/"+r.ID {
			t.Errorf("%s path %s", r.ID, r.Path)
		}
	}
}
