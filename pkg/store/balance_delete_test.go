package store

import (
	"context"
	"errors"
	"testing"

	"github.com/nutmos/expensetrackr/pkg/balance"
	"github.com/nutmos/expensetrackr/pkg/user"
)

// TestDeleteBalanceRemovesTransactions: deleting a balance removes every
// transaction that references it (source, destination, the asset linked by a
// return on investment, its balance adjustments) and reverses their effects
// on the other balances; unrelated transactions stay. A stale version changes
// nothing.
func TestDeleteBalanceRemovesTransactions(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.create(f.tx("expense", "5", "THB", f.pay, nil))                  // pay 995 v2
	f.create(f.tx("transfer", "10", "THB", f.pay, &f.gold))            // pay 985 v3, gold 510 v2
	f.create(f.tx("asset_purchase", "20", "THB", f.card, &f.gold))     // card 120 v2, gold 530 v3
	f.create(f.tx("return_on_investment", "7", "THB", f.pay, &f.gold)) // pay 992 v4 (gold only linked)
	f.create(f.tx("transfer", "30", "THB", f.gold, &f.loan))           // gold 500 v4, loan 4970 v2
	if _, err := f.edit(f.gold, WriteOptions{}, func(in *balance.Input) { in.Balance = dec("600") }); err != nil {
		t.Fatal(err) // gold 600 v5, one adjustment
	}
	f.create(f.tx("transfer", "50", "THB", f.pay, &f.card)) // pay 942 v5, card 70 v3

	count := func() int {
		t.Helper()
		all, err := f.st.List(ctx, f.userUID, ListFilter{})
		if err != nil {
			t.Fatal(err)
		}
		return len(all)
	}
	if n := count(); n != 7 {
		t.Fatalf("setup: %d transactions, want 7", n)
	}

	if _, err := f.st.DeleteBalanceWith(ctx, f.userUID, f.gold.ID, WriteOptions{Version: 4}); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale delete: %v", err)
	}
	if n := count(); n != 7 {
		t.Errorf("stale delete removed transactions: %d left", n)
	}
	f.want(f.gold, "600.00", 5)

	n, err := f.st.DeleteBalanceWith(ctx, f.userUID, f.gold.ID, WriteOptions{Version: 5})
	if err != nil || n != 5 {
		t.Fatalf("delete gold: %d transaction(s), %v; want 5", n, err)
	}
	if _, err := f.st.GetBalance(ctx, f.userUID, f.gold.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("gold still there: %v", err)
	}
	// Each balance is written once with the summed reversal.
	f.want(f.pay, "945.00", 6)   // +10 (transfer back) −7 (return removed)
	f.want(f.card, "50.00", 4)   // −20 (asset purchase removed)
	f.want(f.loan, "5000.00", 3) // +30 debt (repayment from gold removed)
	left, err := f.st.List(ctx, f.userUID, ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 2 {
		t.Fatalf("left %d transactions, want 2 (the expense and the card payment)", len(left))
	}
	for _, e := range left {
		if e.BalanceUID == f.gold.UID || (e.ToBalanceUID != nil && *e.ToBalanceUID == f.gold.UID) {
			t.Errorf("transaction %s still references gold", e.UID)
		}
	}

	// A balance without transactions: nothing else moves.
	if n, err := f.st.DeleteBalance(ctx, f.userUID, f.loan.ID); err != nil || n != 0 {
		t.Errorf("delete loan: %d, %v", n, err)
	}
	f.want(f.pay, "945.00", 6)
}

// TestDeleteBalanceKeepsOtherUsers: only the owner's transactions go.
func TestDeleteBalanceKeepsOtherUsers(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.create(f.tx("expense", "5", "THB", f.pay, nil))
	name := "other"
	u := user.User{Username: &name, DisplayName: "Other"}
	if err := f.st.RegisterUser(ctx, &u, "$2a$04$unused", false); err != nil {
		t.Fatal(err)
	}
	other := &fixture{t: t, st: f.st, userUID: u.UID}
	other.pay = mustBalance(t, balance.Input{Name: "pay", Type: "payment_account", Currency: "THB", Balance: dec("10")})
	if err := f.st.CreateBalance(ctx, u.UID, &other.pay); err != nil {
		t.Fatal(err)
	}
	other.create(other.tx("expense", "1", "THB", other.pay, nil))
	if n, err := f.st.DeleteBalance(ctx, f.userUID, f.pay.ID); err != nil || n != 1 {
		t.Fatalf("delete: %d, %v", n, err)
	}
	if got, err := f.st.List(ctx, u.UID, ListFilter{}); err != nil || len(got) != 1 {
		t.Errorf("other user's transactions: %d, %v", len(got), err)
	}
	other.want(other.pay, "9.00", 2)
}

// TestHideBalance: hiding only flips the flag (version +1); transactions keep
// working on a hidden balance and manual edits keep it hidden.
func TestHideBalance(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	b, err := f.st.SetBalanceHidden(ctx, f.userUID, f.card.ID, WriteOptions{Version: 1}, true)
	if err != nil || !b.Hidden || b.Version != 2 || b.UpdatedAt == nil {
		t.Fatalf("hide: %+v %v", b, err)
	}
	// Same state again: no change, no new version.
	if b, err := f.st.SetBalanceHidden(ctx, f.userUID, f.card.ID, WriteOptions{}, true); err != nil || b.Version != 2 {
		t.Errorf("hide again: v%d %v", b.Version, err)
	}
	if _, err := f.st.SetBalanceHidden(ctx, f.userUID, f.card.ID, WriteOptions{Version: 1}, false); !errors.Is(err, ErrVersionConflict) {
		t.Errorf("stale unhide: %v", err)
	}
	if _, err := f.st.SetBalanceHidden(ctx, f.userUID, 1<<40, WriteOptions{}, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("hide missing: %v", err)
	}

	list, err := f.st.ListBalances(ctx, f.userUID, "")
	if err != nil {
		t.Fatal(err)
	}
	hidden := 0
	for _, x := range list {
		if x.Hidden {
			hidden++
			if x.UID != f.card.UID {
				t.Errorf("%s hidden", x.Name)
			}
		}
	}
	if len(list) != 4 || hidden != 1 {
		t.Errorf("list: %d balances, %d hidden", len(list), hidden)
	}

	// Transactions still move a hidden balance.
	f.create(f.tx("expense", "30", "THB", f.card, nil))
	f.want(f.card, "130.00", 3)
	// A manual edit keeps it hidden.
	got, err := f.edit(f.card, WriteOptions{Version: 3}, func(in *balance.Input) { in.Name = "old card" })
	if err != nil || !got.Hidden || got.Name != "old card" {
		t.Errorf("edit hidden: %+v %v", got, err)
	}
	b, err = f.st.SetBalanceHidden(ctx, f.userUID, f.card.ID, WriteOptions{Version: 4}, false)
	if err != nil || b.Hidden || b.Version != 5 || *b.Debt != "130.00" {
		t.Errorf("unhide: %+v %v", b, err)
	}

	// New balances start visible, whatever the struct says.
	nb := mustBalance(t, balance.Input{Name: "new", Type: "payment_account", Currency: "THB", Balance: dec("1")})
	nb.Hidden = true
	if err := f.st.CreateBalance(ctx, f.userUID, &nb); err != nil {
		t.Fatal(err)
	}
	if got, err := f.st.GetBalance(ctx, f.userUID, nb.ID); err != nil || got.Hidden || nb.Hidden {
		t.Errorf("created hidden: %+v %v", got, err)
	}
}
