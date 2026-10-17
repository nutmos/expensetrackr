package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nutmos/expensetrackr/pkg/balance"
	"github.com/nutmos/expensetrackr/pkg/transaction"
)

// fixClock pins the balance adjustment clock to 2026-10-08 21:30:00 +08:00.
func fixClock(t *testing.T) {
	t.Helper()
	at := time.Date(2026, 10, 8, 21, 30, 0, 123, time.FixedZone("SGT", 8*3600))
	old := clock
	clock = func() time.Time { return at }
	t.Cleanup(func() { clock = old })
}

// edit runs a manual balance edit (the PUT path).
func (f *fixture) edit(b balance.Balance, opts WriteOptions, mut func(*balance.Input)) (balance.Balance, error) {
	f.t.Helper()
	return f.st.UpdateBalanceWith(context.Background(), b.ID, opts, func(cur balance.Balance) (balance.Balance, error) {
		in := cur.Input()
		mut(&in)
		return in.ValidateUpdate(cur.Type)
	})
}

func (f *fixture) adjustments() []transaction.Transaction {
	f.t.Helper()
	got, err := f.st.List(context.Background(), ListFilter{Type: transaction.BalanceAdjustment})
	if err != nil {
		f.t.Fatal(err)
	}
	return got
}

func TestManualEditRecordsAdjustment(t *testing.T) {
	fixClock(t)
	f := newFixture(t)
	cases := []struct {
		b      balance.Balance
		mut    func(*balance.Input)
		amount string
		dir    transaction.Direction
		note   string
		after  string
	}{
		{f.pay, func(in *balance.Input) { in.Balance = dec("950") }, "50.00", transaction.Decrease, "Manual edit of balance: 1000.00 → 950.00", "950.00"},
		{f.gold, func(in *balance.Input) { in.Balance = dec("750.5") }, "250.50", transaction.Increase, "Manual edit of balance: 500.00 → 750.50", "750.50"},
		{f.card, func(in *balance.Input) { in.Debt = dec("175") }, "75.00", transaction.Increase, "Manual edit of debt: 100.00 → 175.00", "175.00"},
		{f.loan, func(in *balance.Input) { in.Debt = dec("-20") }, "5020.00", transaction.Decrease, "Manual edit of debt: 5000.00 → -20.00", "-20.00"},
	}
	for _, c := range cases {
		if _, err := f.edit(c.b, WriteOptions{Version: 1}, c.mut); err != nil {
			t.Fatalf("%s: %v", c.b.Name, err)
		}
		// The edit sets the value exactly once: the adjustment is not applied
		// on top of it (no double counting) and the version moves by one.
		f.want(c.b, c.after, 2)
		var got []transaction.Transaction
		for _, a := range f.adjustments() {
			if a.BalanceUID.Matches(c.b.UID) {
				got = append(got, a)
			}
		}
		if len(got) != 1 {
			t.Fatalf("%s: %d adjustments, want 1", c.b.Name, len(got))
		}
		a := got[0]
		if a.Type != transaction.BalanceAdjustment || a.Amount != c.amount || a.AdjustmentDirection == nil || *a.AdjustmentDirection != c.dir ||
			a.Currency != "THB" || a.Account != c.b.Name || a.Note != c.note || a.SpentAt != "2026-10-08T21:30:00+08:00" ||
			a.CategoryUID != nil || a.ToBalanceUID != nil || a.AdjustsBalances || a.Version != 1 || !looksLikeUUID(string(a.UID)) {
			t.Errorf("%s adjustment: %+v (dir %v)", c.b.Name, a, a.AdjustmentDirection)
		}
		if len(a.Effects()) != 0 {
			t.Errorf("%s: adjustment has effects %v", c.b.Name, a.Effects())
		}
	}
	if n := len(f.adjustments()); n != 4 {
		t.Errorf("%d adjustments in total", n)
	}
	// Ordinary transactions are not affected and do not show up in the filter.
	f.create(f.tx("expense", "10", "THB", f.pay, nil))
	f.want(f.pay, "940.00", 3)
	all, _ := f.st.List(context.Background(), ListFilter{})
	if len(all) != 5 || len(f.adjustments()) != 4 {
		t.Errorf("list %d, adjustments %d", len(all), len(f.adjustments()))
	}
	exp, _ := f.st.List(context.Background(), ListFilter{Type: transaction.Expense})
	if len(exp) != 1 {
		t.Errorf("expense filter: %d", len(exp))
	}
	// Deleting the expense reverses only the expense.
	if err := f.st.Delete(context.Background(), exp[0].ID); err != nil {
		t.Fatal(err)
	}
	f.want(f.pay, "950.00", 4)
}

func TestNoAdjustmentWithoutAmountChange(t *testing.T) {
	f := newFixture(t)
	steps := []struct {
		name string
		b    balance.Balance
		mut  func(*balance.Input)
	}{
		{"rename", f.pay, func(in *balance.Input) { in.Name = "Pay"; in.Description = "main" }},
		{"same value re-sent", f.pay, func(in *balance.Input) { in.Balance = dec("1000.00") }},
		{"limit only", f.card, func(in *balance.Input) { in.Limit = dec("2000") }},
		{"limit only (loan)", f.loan, func(in *balance.Input) { in.Limit = dec("1") }},
		// Currency change (possible only while nothing references the balance):
		// the amounts are in different currencies, so no adjustment.
		{"currency change", f.gold, func(in *balance.Input) { in.Currency = "USD"; in.Balance = dec("600") }},
	}
	for _, s := range steps {
		if _, err := f.edit(s.b, WriteOptions{}, s.mut); err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		if n := len(f.adjustments()); n != 0 {
			t.Fatalf("%s: %d adjustments, want 0", s.name, n)
		}
	}
	// Debt and limit in one edit: exactly one adjustment, for the debt.
	if _, err := f.edit(f.card, WriteOptions{}, func(in *balance.Input) { in.Debt = dec("90"); in.Limit = dec("3000") }); err != nil {
		t.Fatal(err)
	}
	if a := f.adjustments(); len(a) != 1 || a[0].Amount != "10.00" || *a[0].AdjustmentDirection != transaction.Decrease {
		t.Errorf("debt+limit edit: %+v", a)
	}
	// Once an adjustment references the balance, its currency is fixed.
	if _, err := f.edit(f.card, WriteOptions{}, func(in *balance.Input) { in.Currency = "USD" }); !errors.Is(err, ErrBalanceInUse) {
		t.Errorf("currency change after adjustment: %v", err)
	}
}

func TestStaleEditRecordsNothing(t *testing.T) {
	f := newFixture(t)
	f.create(f.tx("expense", "1", "THB", f.pay, nil)) // pay v2
	if _, err := f.edit(f.pay, WriteOptions{Version: 1}, func(in *balance.Input) { in.Balance = dec("5") }); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale edit: %v", err)
	}
	// A rejected edit (validation error) records nothing either.
	if _, err := f.edit(f.pay, WriteOptions{Version: 2}, func(in *balance.Input) { in.Balance = dec("x") }); err == nil {
		t.Fatal("invalid edit accepted")
	}
	if n := len(f.adjustments()); n != 0 {
		t.Errorf("%d adjustments after rejected edits", n)
	}
	f.want(f.pay, "999.00", 2)
}

func TestAdjustmentIsReadOnly(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.edit(f.pay, WriteOptions{}, func(in *balance.Input) { in.Balance = dec("900") }); err != nil {
		t.Fatal(err)
	}
	adj := f.adjustments()[0]
	if _, err := f.st.UpdateWith(ctx, adj.ID, WriteOptions{Version: 1}, func(cur transaction.Transaction) (transaction.Transaction, error) {
		cur.Note = "x"
		return cur, nil
	}); !errors.Is(err, ErrReadOnly) {
		t.Errorf("update adjustment: %v", err)
	}
	if err := f.st.DeleteWith(ctx, adj.ID, WriteOptions{}); !errors.Is(err, ErrReadOnly) {
		t.Errorf("delete adjustment: %v", err)
	}
	// Neither can one be created, nor an ordinary transaction turned into one.
	e := f.tx("expense", "5", "THB", f.pay, nil)
	e.Type = transaction.BalanceAdjustment
	if err := f.st.Create(ctx, &e); !errors.Is(err, ErrReadOnly) {
		t.Errorf("create adjustment: %v", err)
	}
	exp := f.create(f.tx("expense", "5", "THB", f.pay, nil))
	if _, err := f.st.Update(ctx, exp.ID, func(cur transaction.Transaction) (transaction.Transaction, error) {
		cur.Type = transaction.BalanceAdjustment
		return cur, nil
	}); !errors.Is(err, ErrReadOnly) {
		t.Errorf("update to adjustment: %v", err)
	}
	if got, _ := f.st.Get(ctx, adj.ID); got.Version != 1 || got.Note != adj.Note {
		t.Errorf("adjustment changed: %+v", got)
	}
	f.want(f.pay, "895.00", 3)

	// The table itself refuses adjustment rows that would move a balance or
	// carry a destination/category, and a direction on other types.
	for _, q := range []string{
		`INSERT INTO transactions (uid, type, amount_minor, amount_scale, currency, balance_uid, account, spent_at, spent_at_unix, created_at, balance_applied, adjustment_direction)
		 VALUES ('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa1', 'balance_adjustment', 1, 2, 'THB', '` + f.pay.UID + `', 'pay', 'x', 0, 'x', 1, 'increase')`,
		`INSERT INTO transactions (uid, type, amount_minor, amount_scale, currency, balance_uid, account, spent_at, spent_at_unix, created_at, adjustment_direction)
		 VALUES ('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa2', 'balance_adjustment', 1, 2, 'THB', '` + f.pay.UID + `', 'pay', 'x', 0, 'x', NULL)`,
		`INSERT INTO transactions (uid, type, amount_minor, amount_scale, currency, balance_uid, account, spent_at, spent_at_unix, created_at, adjustment_direction)
		 VALUES ('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa3', 'expense', 1, 2, 'THB', '` + f.pay.UID + `', 'pay', 'x', 0, 'x', 'increase')`,
		`INSERT INTO transactions (uid, type, amount_minor, amount_scale, currency, balance_uid, account, spent_at, spent_at_unix, created_at, adjustment_direction, to_balance_uid)
		 VALUES ('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa4', 'balance_adjustment', 1, 2, 'THB', '` + f.pay.UID + `', 'pay', 'x', 0, 'x', 'increase', '` + f.card.UID + `')`,
		`INSERT INTO transactions (uid, type, amount_minor, amount_scale, currency, balance_uid, account, spent_at, spent_at_unix, created_at, adjustment_direction)
		 VALUES ('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa5', 'balance_adjustment', 1, 2, 'THB', '` + f.pay.UID + `', 'pay', 'x', 0, 'x', 'sideways')`,
	} {
		if _, err := f.st.db.Exec(q); err == nil || !strings.Contains(err.Error(), "CHECK") {
			t.Errorf("CHECK did not reject: %v\n%s", err, q)
		}
	}
}
