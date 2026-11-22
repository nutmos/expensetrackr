package store

import (
	"context"
	"testing"

	"github.com/nutmos/expensetrackr/pkg/money"
	"github.com/nutmos/expensetrackr/pkg/report"
	"github.com/nutmos/expensetrackr/pkg/transaction"
)

// withSale sets the gain (> 0) or loss (< 0) in THB minor units (0 = none),
// the entire-asset flag and, if cat, the test category on e.
func withSale(e transaction.Transaction, gainLoss int64, entire, cat bool) transaction.Transaction {
	e.GainLoss, e.GainLossMinor = nil, nil
	if gainLoss != 0 {
		a := money.FormatAmount(gainLoss, 2)
		e.GainLoss, e.GainLossMinor = &a, &gainLoss
	}
	e.EntireAssetSale = entire
	e.CategoryUID = nil
	if cat {
		c := transaction.UID(testCategoryUID)
		e.CategoryUID = &c
	}
	return e
}

// TestAssetSale: the account receives principal + gain (or - loss), the asset
// goes down by the principal; edits and deletes reverse exactly; the fields
// round-trip; gains and losses are in the report.
func TestAssetSale(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	e := f.create(withSale(f.tx("asset_sale", "200", "THB", f.pay, &f.gold), 5000, false, true))
	f.want(f.pay, "1250.00", 2) // 1000 + 200 + 50
	f.want(f.gold, "300.00", 2) // 500 - 200

	got, err := f.st.GetByUID(ctx, f.userUID, e.UID)
	if err != nil {
		t.Fatal(err)
	}
	if got.GainLoss == nil || *got.GainLoss != "50.00" || *got.GainLossMinor != 5000 || got.EntireAssetSale {
		t.Fatalf("stored = gain_loss %v/%v entire %v", got.GainLoss, got.GainLossMinor, got.EntireAssetSale)
	}

	rows, err := f.st.SumFlows(ctx, f.userUID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Type != transaction.Income || rows[0].SumMinor.Int64() != 5000 {
		t.Errorf("SumFlows gain = %+v, want one income row of 5000", rows)
	}

	// Turn it into a loss of 30 on a card: the card's debt goes down by 170.
	if _, err := f.st.UpdateWith(ctx, f.userUID, e.ID, WriteOptions{}, func(transaction.Transaction) (transaction.Transaction, error) {
		return withSale(f.tx("asset_sale", "200", "THB", f.card, &f.gold), -3000, false, false), nil
	}); err != nil {
		t.Fatal(err)
	}
	f.want(f.pay, "1000.00", 3)
	f.want(f.card, "-70.00", 2) // 100 - 170: in credit
	f.want(f.gold, "300.00", 2) // unchanged net

	rows, err = f.st.SumFlows(ctx, f.userUID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	flows := report.BuildIncomeExpense(rows, nil)
	if len(flows) != 1 || flows[0].Expense != "30.00" || flows[0].Income != "0.00" {
		t.Errorf("report after loss = %+v", flows)
	}

	if err := f.st.DeleteWith(ctx, f.userUID, e.ID, WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	f.want(f.card, "100.00", 3)
	f.want(f.gold, "500.00", 3)

	// A sale at cost (no gain or loss) is not in the report.
	f.create(f.tx("asset_sale", "10", "THB", f.pay, &f.gold))
	if rows, err := f.st.SumFlows(ctx, f.userUID, nil, nil); err != nil || len(rows) != 0 {
		t.Errorf("SumFlows without gain/loss = %+v, %v", rows, err)
	}
}

// TestAssetSaleEntireHidesAsset: an entire asset sale hides the asset, on
// create and on an edit that sets the flag; deleting it does not show it.
func TestAssetSaleEntireHidesAsset(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	hidden := func() bool {
		t.Helper()
		b, err := f.st.GetBalanceByUID(ctx, f.userUID, f.gold.UID)
		if err != nil {
			t.Fatal(err)
		}
		return b.Hidden
	}

	e := f.create(f.tx("asset_sale", "100", "THB", f.pay, &f.gold))
	if hidden() {
		t.Fatal("partial sale hid the asset")
	}
	if _, err := f.st.UpdateWith(ctx, f.userUID, e.ID, WriteOptions{}, func(transaction.Transaction) (transaction.Transaction, error) {
		return withSale(f.tx("asset_sale", "500", "THB", f.pay, &f.gold), 0, true, false), nil
	}); err != nil {
		t.Fatal(err)
	}
	if !hidden() {
		t.Fatal("entire sale did not hide the asset")
	}
	f.want(f.gold, "0.00", 4) // two moves + hide
	if err := f.st.DeleteWith(ctx, f.userUID, e.ID, WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	if !hidden() {
		t.Error("delete showed the asset again")
	}
	f.want(f.gold, "500.00", 5)

	// Bulk create goes through the same path.
	if _, err := f.st.SetBalanceHidden(ctx, f.userUID, f.gold.ID, WriteOptions{}, false); err != nil {
		t.Fatal(err)
	}
	res, err := f.st.CreateBulk(ctx, f.userUID, []BulkItem{{Tx: withSale(f.tx("asset_sale", "500", "THB", f.pay, &f.gold), 0, true, false)}}, BulkOptions{})
	if err != nil || res.Results[0].Status != BulkCreated {
		t.Fatalf("bulk = %+v, %v", res.Results, err)
	}
	if !hidden() {
		t.Error("bulk entire sale did not hide the asset")
	}
}

// TestAssetSaleInvariants: the store refuses sale fields on other types, a
// zero gain_loss, a loss above the principal and a category without a gain
// or loss.
func TestAssetSaleInvariants(t *testing.T) {
	f := newFixture(t)
	zero := f.tx("asset_sale", "10", "THB", f.pay, &f.gold)
	z := int64(0)
	zero.GainLossMinor = &z
	cases := map[string]transaction.Transaction{
		"gain on an expense":         withSale(f.tx("expense", "10", "THB", f.pay, nil), 100, false, false),
		"entire flag on a transfer":  withSale(f.tx("transfer", "10", "THB", f.pay, &f.gold), 0, true, false),
		"zero gain_loss":             zero,
		"loss above principal":       withSale(f.tx("asset_sale", "10", "THB", f.pay, &f.gold), -1001, false, false),
		"category without gain/loss": withSale(f.tx("asset_sale", "10", "THB", f.pay, &f.gold), 0, false, true),
	}
	for name, e := range cases {
		if err := f.st.Create(context.Background(), f.userUID, &e); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	f.want(f.pay, "1000.00", 1)
}

// TestMigrateV7ToV8: a v7 database gains the asset_sale type and columns,
// keeps its rows, and ends up with the same schema as a fresh one.
func TestMigrateV7ToV8(t *testing.T) {
	url := testURL(t)
	buildV1(t, url)
	db := rawDB(t, url)
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations[1:7] {
		if err := m(ctx, tx); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO schema_migrations (version) VALUES (2), (3), (4), (5), (6), (7)`); err != nil {
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
	if err := insert("income", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := insert("asset_sale", "", ""); err == nil {
		t.Fatal("v7 accepted asset_sale; the test setup is wrong")
	}

	st, err := Open(url)
	if err != nil {
		t.Fatalf("open v7: %v", err)
	}
	defer st.Close()
	if v := schemaVersion(t, url); v != SchemaVersion {
		t.Errorf("schema version after upgrade = %d, want %d", v, SchemaVersion)
	}
	if err := insert("asset_sale", ", gain_loss_minor, entire_asset_sale, category_uid", ", -100, true, '"+testCategoryUID+"'"); err != nil {
		t.Errorf("asset_sale after upgrade: %v", err)
	}
	for name, c := range map[string]struct{ typ, cols, vals string }{
		"gain on income":             {"income", ", gain_loss_minor", ", 10"},
		"entire flag on income":      {"income", ", entire_asset_sale", ", true"},
		"zero gain_loss":             {"asset_sale", ", gain_loss_minor", ", 0"},
		"loss above principal":       {"asset_sale", ", gain_loss_minor", ", -101"},
		"category without gain/loss": {"asset_sale", ", category_uid", ", '" + testCategoryUID + "'"},
	} {
		if err := insert(c.typ, c.cols, c.vals); err == nil {
			t.Errorf("%s accepted", name)
		}
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
