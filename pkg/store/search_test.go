package store

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/nutmos/expensetrackr/pkg/balance"
	"github.com/nutmos/expensetrackr/pkg/category"
	"github.com/nutmos/expensetrackr/pkg/transaction"
	"github.com/nutmos/expensetrackr/pkg/user"
)

func TestSearchNumber(t *testing.T) {
	for in, want := range map[string]string{"120": "120", "1,250.50": "1250.50", ".5": ".5", "12.": "12."} {
		if got, ok := searchNumber(in); !ok || got != want {
			t.Errorf("%q: %q %v, want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", ".", "-", ",", "12a", "1.2.3", "1-2", "1e5", strings.Repeat("9", 31)} {
		if got, ok := searchNumber(in); ok {
			t.Errorf("%q: %q is a number", in, got)
		}
	}
}

// searchTx creates a transaction for userUID from in (spent_at and the
// balance names filled in).
func searchTx(t *testing.T, st *Store, userUID string, in transaction.CreateInput, from balance.Balance, to *balance.Balance) string {
	t.Helper()
	in.BalanceUID, in.SpentAt = string(from.UID), "2026-10-08T12:00:00+08:00"
	if to != nil {
		in.ToBalanceUID = string(to.UID)
	}
	e, err := in.Validate()
	if err != nil {
		t.Fatal(err)
	}
	e.Account = from.Name
	if to != nil {
		e.ToAccount = &to.Name
	}
	if err := st.Create(context.Background(), userUID, &e); err != nil {
		t.Fatal(err)
	}
	return string(e.UID)
}

// TestListSearch covers ListFilter.Search: each word must match the note,
// a balance name (snapshot or current), the category name, the import
// reference, the type, a currency or (for numbers) an amount.
func TestListSearch(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	st, me := f.st, f.userUID

	food := category.Category{Name: "Food & Drinks", Type: category.TypeExpense}
	salary := category.Category{Name: "Salary", Type: category.TypeIncome}
	for _, c := range []*category.Category{&food, &salary} {
		if err := st.CreateCategory(ctx, me, c); err != nil {
			t.Fatal(err)
		}
	}
	ref := "STMT-2026-10"
	lunch := searchTx(t, st, me, transaction.CreateInput{Type: "expense", Amount: "120.50", Currency: "THB", CategoryUID: string(food.UID), Note: "Lunch at 50% off_sale"}, f.pay, nil)
	coffee := searchTx(t, st, me, transaction.CreateInput{Type: "expense", Amount: "85", Currency: "THB", CategoryUID: string(food.UID), Note: "Coffee", ForeignAmount: "2.50", ForeignCurrency: "USD"}, f.card, nil)
	move := searchTx(t, st, me, transaction.CreateInput{Type: "transfer", Amount: "1000", ToAmount: "1000", Currency: "THB"}, f.pay, &f.gold)
	pay := searchTx(t, st, me, transaction.CreateInput{Type: "income", Amount: "3000", Currency: "THB", CategoryUID: string(salary.UID), ImportRef: &ref}, f.pay, nil)

	// A renamed balance is found by its old (snapshot) and its new name.
	// (sealed like the store does, so this also works with encryption on).
	bullion, err := st.seal("Bullion")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE balances SET name = $1 WHERE uid = $2`, bullion, f.gold.UID); err != nil {
		t.Fatal(err)
	}

	// Another user's matching rows never show up.
	name := "other"
	u := user.User{Username: &name, DisplayName: "Other"}
	if err := st.RegisterUser(ctx, &u, "$2a$04$unused", false); err != nil {
		t.Fatal(err)
	}
	ob := mustBalance(t, balance.Input{Name: "card gold pay", Type: "payment_account", Currency: "THB", Balance: dec("1000")})
	if err := st.CreateBalance(ctx, u.UID, &ob); err != nil {
		t.Fatal(err)
	}
	oc := category.Category{Name: "Food", Type: category.TypeExpense}
	if err := st.CreateCategory(ctx, u.UID, &oc); err != nil {
		t.Fatal(err)
	}
	searchTx(t, st, u.UID, transaction.CreateInput{Type: "expense", Amount: "120.50", Currency: "THB", CategoryUID: string(oc.UID), Note: "Lunch coffee STMT"}, ob, nil)

	cases := map[string][]string{
		"lunch":        {lunch},
		"LUNCH":        {lunch},
		"food":         {lunch, coffee},
		"drinks lunch": {lunch},
		"lunch coffee": nil,
		"50%":          {lunch},
		"%":            {lunch},
		"_sale":        {lunch},
		"f_":           {lunch},
		"card":         {coffee},
		"usd":          {coffee},
		"2.50":         {coffee},
		"85":           {coffee},
		"85.00":        {coffee},
		"120.5":        {lunch},
		"1,000":        {move},
		"gold":         {move},
		"bullion":      {move},
		"transfer":     {move},
		"stmt-2026":    {pay},
		"salary pay":   {pay},
		"income":       {pay},
		"nothing":      nil,
	}
	for q, want := range cases {
		got, err := withoutBeginning(st.List(ctx, me, ListFilter{Search: q}))
		if err != nil {
			t.Fatalf("%q: %v", q, err)
		}
		var uids []string
		for _, e := range got {
			uids = append(uids, string(e.UID))
		}
		sort.Strings(uids)
		sort.Strings(want)
		if strings.Join(uids, ",") != strings.Join(want, ",") {
			t.Errorf("%q: got %v, want %v", q, uids, want)
		}
	}

	// Combines with the other filters and pages.
	page, next, err := st.ListPage(ctx, me, ListFilter{Search: "food", Type: transaction.Expense, Limit: 1})
	if err != nil || len(page) != 1 || next == nil {
		t.Fatalf("page 1: %v %v %v", page, next, err)
	}
	page2, next, err := st.ListPage(ctx, me, ListFilter{Search: "food", Type: transaction.Expense, Limit: 1, After: next})
	if err != nil || len(page2) != 1 || next != nil || page2[0].UID == page[0].UID {
		t.Fatalf("page 2: %v %v %v", page2, next, err)
	}
}
