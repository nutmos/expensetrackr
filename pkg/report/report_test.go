package report

import (
	"math/big"
	"testing"
	"time"

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

func TestBuildBalanceTransactions(t *testing.T) {
	ptr := func(v int64) *int64 { return &v }
	str := func(s string) *string { return &s }
	day := func(d int) time.Time { return time.Date(2026, 11, d, 12, 0, 0, 0, time.UTC) }
	// A USD card with a debt of 40.00 now.
	card := balance.Balance{UID: "c", Name: "Card", Type: balance.TypeCreditCard, Currency: "USD", Scale: 2, DebtMinor: ptr(4000)}
	inc := transaction.Increase
	dec := transaction.Decrease
	inRange := []transaction.Transaction{
		// A manual edit raised the debt by 10.00 (by amount: debt, not value).
		{UID: "a1", Type: transaction.BalanceAdjustment, BalanceUID: "c", AmountMinor: 1000, Amount: "10.00", Currency: "USD", AdjustmentDirection: &inc, SpentTime: day(2)},
		// 3000.00 THB paid into it, credited as 90.00 USD.
		{UID: "t1", Type: transaction.Transfer, BalanceUID: "s", Account: "THB acct", ToBalanceUID: uid("c"), ToAccount: str("Card"),
			AmountMinor: 300000, Amount: "3000.00", Currency: "THB", ToAmountMinor: ptr(9000), ToCurrency: str("USD"), SpentTime: day(3)},
		{UID: "a2", Type: transaction.BalanceAdjustment, BalanceUID: "c", AmountMinor: 500, Amount: "5.00", Currency: "USD", AdjustmentDirection: &dec, SpentTime: day(1)},
	}
	after := []transaction.Transaction{
		{UID: "e1", Type: transaction.Expense, BalanceUID: "c", AmountMinor: 2500, Amount: "25.00", Currency: "USD", SpentTime: day(20)},
	}
	r := BuildBalanceTransactions(card, inRange, after, nil)
	// closing debt = 40 - 25 = 15; net value change in range = +5 - 10 + 90 = +85; opening debt = 15 + 85 = 100.
	if r.Balance.Amount != "40.00" || r.ClosingAmount != "15.00" || r.OpeningAmount != "100.00" ||
		r.MoneyIn != "95.00" || r.MoneyOut != "10.00" || r.Net != "85.00" || r.Count != 3 {
		t.Fatalf("totals: %+v", r)
	}
	want := []struct{ uid, after string }{{"a2", "95.00"}, {"a1", "105.00"}, {"t1", "15.00"}}
	for i, w := range want {
		e := r.Transactions[i]
		if string(e.UID) != w.uid || e.AmountAfter != w.after {
			t.Errorf("row %d: %s after %s, want %s after %s", i, e.UID, e.AmountAfter, w.uid, w.after)
		}
	}
	if e := r.Transactions[2]; e.MoneyIn == nil || *e.MoneyIn != "90.00" || e.MoneyOut != nil || e.Counterparty == nil || *e.Counterparty != "THB acct" || e.Currency != "THB" {
		t.Errorf("transfer row: %+v", e)
	}
	if e := r.Transactions[1]; e.MoneyOut == nil || *e.MoneyOut != "10.00" || e.Counterparty != nil {
		t.Errorf("adjustment row: %+v", e)
	}
}
