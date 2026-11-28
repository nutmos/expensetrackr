package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nutmos/expensetrackr/pkg/transaction"
)

// TestCreateBulkStore covers the store layer directly: savepoint rollback of a
// failing item, in-batch duplicates, atomic rollback and dry run.
func TestCreateBulkStore(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	ref := func(e transaction.Transaction, r string) transaction.Transaction { e.ImportRef = &r; return e }
	bad := f.tx("expense", "1", "USD", f.pay, nil) // currency mismatch, detected in the tx
	items := []BulkItem{
		{Tx: ref(f.tx("expense", "10", "THB", f.pay, nil), "a")},
		{Tx: bad},
		{Tx: ref(f.tx("expense", "11", "THB", f.pay, nil), "a")},
		{Err: errors.New("pre-validation failed")},
	}
	payBefore, _ := f.amount(f.pay)
	out, err := f.st.CreateBulk(ctx, f.userUID, items, BulkOptions{Atomic: false})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range out.Results {
		got = append(got, string(r.Status))
	}
	if strings.Join(got, ",") != "created,failed,skipped,failed" || !out.Committed {
		t.Fatalf("statuses %v committed %v", got, out.Committed)
	}
	if out.Results[2].ExistingUID != string(out.Results[0].Tx.UID) {
		t.Errorf("existing uid")
	}
	payAfter, _ := f.amount(f.pay)
	if payBefore == payAfter {
		t.Errorf("balance did not move")
	}
	// Atomic with a failure and dry run write nothing.
	for _, opts := range []BulkOptions{{Atomic: true}, {Atomic: false, DryRun: true}} {
		out, err := f.st.CreateBulk(ctx, f.userUID, []BulkItem{{Tx: f.tx("expense", "7", "THB", f.pay, nil)}, {Tx: bad}}, opts)
		if err != nil || out.Committed {
			t.Errorf("%+v: committed %v err %v", opts, out.Committed, err)
		}
		if now, _ := f.amount(f.pay); now != payAfter {
			t.Errorf("%+v: balance moved %s -> %s", opts, payAfter, now)
		}
	}
	list, _ := withoutBeginning(f.st.List(ctx, f.userUID, ListFilter{}))
	if len(list) != 1 {
		t.Errorf("rows %d, want 1", len(list))
	}
}

// TestCreateBulkUnexpectedItemError: an item error that is not bad input
// (here an unknown currency, which the API rejects before the store) fails
// the whole call and writes nothing, even when the batch is not atomic, so
// the caller's 500 never hides rows that were committed.
func TestCreateBulkUnexpectedItemError(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	odd := f.tx("expense", "1", "THB", f.pay, nil)
	odd.Currency = "XXX"
	payBefore, _ := f.amount(f.pay)
	for _, opts := range []BulkOptions{{Atomic: false}, {Atomic: true}, {DryRun: true}} {
		items := []BulkItem{{Tx: f.tx("expense", "10", "THB", f.pay, nil)}, {Tx: odd}, {Tx: f.tx("expense", "11", "THB", f.pay, nil)}}
		out, err := f.st.CreateBulk(ctx, f.userUID, items, opts)
		if err == nil || out.Committed {
			t.Errorf("%+v: committed %v err %v, want an error", opts, out.Committed, err)
		}
		if now, _ := f.amount(f.pay); now != payBefore {
			t.Errorf("%+v: balance moved %s -> %s", opts, payBefore, now)
		}
	}
	// A failed invariant check is unexpected too.
	badFX := f.tx("expense", "1", "THB", f.pay, nil)
	rate := "1"
	badFX.ExchangeRate = &rate
	if _, err := f.st.CreateBulk(ctx, f.userUID, []BulkItem{{Tx: f.tx("expense", "10", "THB", f.pay, nil)}, {Tx: badFX}}, BulkOptions{}); err == nil {
		t.Error("invariant failure: want an error")
	}
	if list, _ := withoutBeginning(f.st.List(ctx, f.userUID, ListFilter{})); len(list) != 0 {
		t.Errorf("rows %d, want 0", len(list))
	}
}
