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
	out, err := f.st.CreateBulk(ctx, items, BulkOptions{Atomic: false})
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
		out, err := f.st.CreateBulk(ctx, []BulkItem{{Tx: f.tx("expense", "7", "THB", f.pay, nil)}, {Tx: bad}}, opts)
		if err != nil || out.Committed {
			t.Errorf("%+v: committed %v err %v", opts, out.Committed, err)
		}
		if now, _ := f.amount(f.pay); now != payAfter {
			t.Errorf("%+v: balance moved %s -> %s", opts, payAfter, now)
		}
	}
	list, _ := f.st.List(ctx, ListFilter{})
	if len(list) != 1 {
		t.Errorf("rows %d, want 1", len(list))
	}
}
