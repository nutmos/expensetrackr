package store

import (
	"context"
	"testing"

	"github.com/nutmos/expensetrackr/pkg/money"
	"github.com/nutmos/expensetrackr/pkg/report"
	"github.com/nutmos/expensetrackr/pkg/transaction"
)

// withCharges sets interest and fees (THB minor units; 0 = none) on e.
func withCharges(e transaction.Transaction, interest, fee int64, cat bool) transaction.Transaction {
	set := func(m int64) (*string, *int64) {
		if m == 0 {
			return nil, nil
		}
		a := money.FormatAmount(m, 2)
		return &a, &m
	}
	e.InterestAmount, e.InterestAmountMinor = set(interest)
	e.FeeAmount, e.FeeAmountMinor = set(fee)
	if cat {
		c := transaction.UID(testCategoryUID)
		e.CategoryUID = &c
	}
	return e
}

// TestRepayDebtCharges: the account pays principal + interest + fees, the
// debt goes down by the principal only; edits and deletes reverse exactly,
// and the charges round-trip.
func TestRepayDebtCharges(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	e := f.create(withCharges(f.tx("repay_debt", "60", "THB", f.pay, &f.card), 1250, 300, true))
	f.want(f.pay, "924.50", 2) // 1000 - (60 + 12.50 + 3)
	f.want(f.card, "40.00", 2) // 100 - 60

	got, err := f.st.GetByUID(ctx, f.userUID, e.UID)
	if err != nil {
		t.Fatal(err)
	}
	if got.InterestAmount == nil || *got.InterestAmount != "12.50" || *got.InterestAmountMinor != 1250 ||
		got.FeeAmount == nil || *got.FeeAmount != "3.00" || *got.FeeAmountMinor != 300 ||
		got.CategoryUID == nil || string(*got.CategoryUID) != testCategoryUID {
		t.Fatalf("stored = interest %v/%v fee %v/%v cat %v", got.InterestAmount, got.InterestAmountMinor, got.FeeAmount, got.FeeAmountMinor, got.CategoryUID)
	}

	// Drop the fee, keep the interest: only the account moves.
	if _, err := f.st.UpdateWith(ctx, f.userUID, e.ID, WriteOptions{}, func(transaction.Transaction) (transaction.Transaction, error) {
		return withCharges(f.tx("repay_debt", "60", "THB", f.pay, &f.card), 1250, 0, true), nil
	}); err != nil {
		t.Fatal(err)
	}
	f.want(f.pay, "927.50", 3)
	f.want(f.card, "40.00", 2)

	rows, err := f.st.SumFlows(ctx, f.userUID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Type != transaction.Expense || rows[0].SumMinor.Int64() != 1250 || rows[0].Count != 1 ||
		rows[0].CategoryUID == nil || string(*rows[0].CategoryUID) != testCategoryUID {
		t.Errorf("SumFlows = %+v, want one expense row of 1250 in the test category", rows)
	}
	flows := report.BuildIncomeExpense(rows, nil)
	if len(flows) != 1 || flows[0].Expense != "12.50" {
		t.Errorf("report = %+v", flows)
	}

	if err := f.st.DeleteWith(ctx, f.userUID, e.ID, WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	f.want(f.pay, "1000.00", 4)
	f.want(f.card, "100.00", 3)

	// A repayment without charges is not in the report.
	f.create(f.tx("repay_debt", "10", "THB", f.pay, &f.card))
	if rows, err := f.st.SumFlows(ctx, f.userUID, nil, nil); err != nil || len(rows) != 0 {
		t.Errorf("SumFlows without charges = %+v, %v", rows, err)
	}
}

// TestRepayDebtChargesInvariants: the store refuses charges on other types
// and a category on a repay_debt without charges.
func TestRepayDebtChargesInvariants(t *testing.T) {
	f := newFixture(t)
	cases := map[string]transaction.Transaction{
		"interest on an expense":   withCharges(f.tx("expense", "10", "THB", f.pay, nil), 100, 0, false),
		"fee on a transfer":        withCharges(f.tx("transfer", "10", "THB", f.pay, &f.gold), 0, 100, false),
		"category without charges": withCharges(f.tx("repay_debt", "10", "THB", f.pay, &f.card), 0, 0, true),
	}
	for name, e := range cases {
		if err := f.st.Create(context.Background(), f.userUID, &e); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	f.want(f.pay, "1000.00", 1)
}

// TestMigrateV4ToV5: a v4 database gains the charge columns, keeps its rows,
// and ends up with the same schema as a fresh one.
func TestMigrateV4ToV5(t *testing.T) {
	url := testURL(t)
	buildV1(t, url)
	db := rawDB(t, url)
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations[1:4] {
		if err := m(ctx, tx); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO schema_migrations (version) VALUES (2), (3), (4)`); err != nil {
		t.Fatal(err)
	}
	const owner = "00000000-0000-4000-8000-000000000001"
	if _, err := db.Exec(`INSERT INTO users (uid, display_name, created_at) VALUES ($1, 'Owner', now())`, owner); err != nil {
		t.Fatal(err)
	}
	insert := func(typ, extraCols, extraVals string) error {
		_, err := db.Exec(`INSERT INTO transactions (uid, user_uid, type, amount_minor, amount_scale, currency, balance_uid, account, spent_at, spent_at_utc, created_at`+extraCols+`)
			VALUES (gen_random_uuid(), $1, $2, 100, 2, 'THB', '00000000-0000-4000-8000-0000000000b1', 'Cash', '2026-10-08T12:00:00+08:00', now(), now()`+extraVals+`)`, owner, typ)
		return err
	}
	if err := insert("repay_debt", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := insert("repay_debt", ", interest_amount_minor", ", 50"); err == nil {
		t.Fatal("v4 accepted interest_amount_minor; the test setup is wrong")
	}

	st, err := Open(url)
	if err != nil {
		t.Fatalf("open v4: %v", err)
	}
	defer st.Close()
	if v := schemaVersion(t, url); v != SchemaVersion {
		t.Errorf("schema version after upgrade = %d, want %d", v, SchemaVersion)
	}
	if err := insert("repay_debt", ", interest_amount_minor, fee_amount_minor, category_uid", ", 50, 10, '"+testCategoryUID+"'"); err != nil {
		t.Errorf("charges after upgrade: %v", err)
	}
	if err := insert("expense", ", fee_amount_minor", ", 10"); err == nil {
		t.Error("fee on an expense accepted")
	}
	if err := insert("repay_debt", ", interest_amount_minor", ", 0"); err == nil {
		t.Error("zero interest accepted")
	}
	if err := insert("repay_debt", ", category_uid", ", '"+testCategoryUID+"'"); err == nil {
		t.Error("category on a repay_debt without charges accepted")
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM transactions`).Scan(&n); err != nil || n != 2 {
		t.Errorf("rows = %d (%v), want 2", n, err)
	}

	fresh := testURL(t)
	fst, err := Open(fresh)
	if err != nil {
		t.Fatal(err)
	}
	fst.Close()
	if got, want := portableSchema(t, url), portableSchema(t, fresh); got != want {
		t.Errorf("upgraded schema differs from a fresh one:\n got %v\nwant %v", got, want)
	}
}
