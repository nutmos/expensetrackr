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

// beginningOf returns the beginning balance of b.
func (f *fixture) beginningOf(b balance.Balance) transaction.Transaction {
	f.t.Helper()
	list, err := f.st.List(context.Background(), f.userUID, ListFilter{Type: transaction.BeginningBalance, BalanceUID: b.UID})
	if err != nil {
		f.t.Fatal(err)
	}
	if len(list) != 1 {
		f.t.Fatalf("%s: %d beginning balances, want 1", b.Name, len(list))
	}
	return list[0]
}

// editBeginning replaces the beginning balance of b through UpdateWith.
func (f *fixture) editBeginning(b balance.Balance, in transaction.CreateInput) (transaction.Transaction, error) {
	f.t.Helper()
	cur := f.beginningOf(b)
	next, err := in.ValidateBeginningBalance()
	if err != nil {
		return transaction.Transaction{}, err
	}
	next.Account = b.Name
	return f.st.UpdateWith(context.Background(), f.userUID, cur.ID, WriteOptions{Version: cur.Version}, func(transaction.Transaction) (transaction.Transaction, error) {
		return next, nil
	})
}

func dirOf(e transaction.Transaction) string {
	if e.AdjustmentDirection == nil {
		return ""
	}
	return string(*e.AdjustmentDirection)
}

// TestBeginningBalanceRecordedOnCreate: every new balance gets exactly one
// beginning balance with its starting amount (signed through the
// direction, 0 allowed), dated by the store clock, without moving it again.
func TestBeginningBalanceRecordedOnCreate(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 1, 9, 30, 15, 0, time.FixedZone("", 7*3600))
	f.st.now = func() time.Time { return at }
	mk := func(in balance.Input) balance.Balance {
		b := mustBalance(t, in)
		if err := f.st.CreateBalance(ctx, f.userUID, &b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	over := mk(balance.Input{Name: "overdrawn", Type: "payment_account", Currency: "JPY", Balance: dec("-1500")})
	zero := mk(balance.Input{Name: "new card", Type: "credit_card", Currency: "THB", Debt: dec("0"), Limit: dec("100")})
	for _, c := range []struct {
		b         balance.Balance
		amount    string
		dir, cur  string
		wantValue string
	}{
		{f.pay, "1000.00", "increase", "THB", "1000.00"},
		{f.card, "100.00", "increase", "THB", "100.00"},
		{f.loan, "5000.00", "increase", "THB", "5000.00"},
		{over, "1500", "decrease", "JPY", "-1500"},
		{zero, "0.00", "increase", "THB", "0.00"},
	} {
		e := f.beginningOf(c.b)
		if e.Amount != c.amount || dirOf(e) != c.dir || e.Currency != c.cur || e.Account != c.b.Name || e.Note != "" ||
			e.CategoryUID != nil || e.ToBalanceUID != nil || !e.AdjustsBalances || e.Version != 1 || !isUUIDv4(string(e.UID)) {
			t.Errorf("%s: %+v (dir %s)", c.b.Name, e, dirOf(e))
		}
		if got, _ := f.amount(c.b); got != c.wantValue {
			t.Errorf("%s = %s, want %s (recording must not move it)", c.b.Name, got, c.wantValue)
		}
	}
	if e := f.beginningOf(over); e.SpentAt != "2026-11-01T09:30:15+07:00" {
		t.Errorf("spent_at %s", e.SpentAt)
	}
}

// TestBeginningBalanceReadOnlyRules: it cannot be created directly, no
// transaction changes type to or from it, it cannot move to another balance
// and it cannot be deleted.
func TestBeginningBalanceReadOnlyRules(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	bb := f.beginningOf(f.pay)

	e := f.tx("expense", "5", "THB", f.pay, nil)
	e.Type, e.CategoryUID = transaction.BeginningBalance, nil
	d := transaction.Increase
	e.AdjustmentDirection = &d
	if err := f.st.Create(ctx, f.userUID, &e); !errors.Is(err, ErrReadOnly) {
		t.Errorf("create: %v", err)
	}
	if _, err := f.st.UpdateWith(ctx, f.userUID, bb.ID, WriteOptions{}, func(transaction.Transaction) (transaction.Transaction, error) {
		return f.tx("expense", "5", "THB", f.pay, nil), nil
	}); !errors.Is(err, ErrReadOnly) {
		t.Errorf("change type: %v", err)
	}
	exp := f.create(f.tx("expense", "5", "THB", f.pay, nil))
	if _, err := f.st.UpdateWith(ctx, f.userUID, exp.ID, WriteOptions{}, func(cur transaction.Transaction) (transaction.Transaction, error) {
		cur.Type, cur.CategoryUID, cur.AdjustmentDirection = transaction.BeginningBalance, nil, &d
		return cur, nil
	}); !errors.Is(err, ErrReadOnly) {
		t.Errorf("into beginning balance: %v", err)
	}
	_, err := f.editBeginning(f.pay, transaction.CreateInput{Amount: "1000", Currency: "THB", BalanceUID: string(f.gold.UID),
		SpentAt: "2026-10-01T00:00:00Z", AdjustmentDirection: sp("increase")})
	if fieldErr(t, err, "balance_uid") != transaction.ErrBeginningBalanceMove {
		t.Errorf("move: %v", err)
	}
	if err := f.st.DeleteWith(ctx, f.userUID, bb.ID, WriteOptions{}); !errors.Is(err, ErrUndeletable) {
		t.Errorf("delete: %v", err)
	}
	if got := f.beginningOf(f.pay); got.Version != bb.Version+1 { // moved back once by the expense
		t.Errorf("after refused writes: v%d", got.Version)
	}
	f.want(f.pay, "995.00", 2)
}

// TestBeginningBalanceEdit: editing the amount or direction moves the
// balance by the difference, like a balance adjustment; zero is allowed.
func TestBeginningBalanceEdit(t *testing.T) {
	f := newFixture(t)
	in := func(amount, dir string) transaction.CreateInput {
		return transaction.CreateInput{Amount: transaction.DecimalInput(amount), Currency: "THB", BalanceUID: string(f.pay.UID),
			SpentAt: "2026-10-01T08:00:00+07:00", Note: "opening", AdjustmentDirection: sp(dir)}
	}
	got, err := f.editBeginning(f.pay, in("1200", "increase"))
	if err != nil || got.Amount != "1200.00" || got.Note != "opening" || got.SpentAt != "2026-10-01T08:00:00+07:00" || got.Version != 2 {
		t.Fatalf("edit: %+v %v", got, err)
	}
	f.want(f.pay, "1200.00", 2)
	if _, err := f.editBeginning(f.pay, in("0", "increase")); err != nil {
		t.Fatal(err)
	}
	f.want(f.pay, "0.00", 3)
	if _, err := f.editBeginning(f.pay, in("50", "decrease")); err != nil {
		t.Fatal(err)
	}
	f.want(f.pay, "-50.00", 4)
	// A liability: increase is debt.
	card := f.beginningOf(f.card)
	if _, err := f.editBeginning(f.card, transaction.CreateInput{Amount: "250", Currency: "THB", BalanceUID: string(f.card.UID),
		SpentAt: card.SpentAt, AdjustmentDirection: sp("increase")}); err != nil {
		t.Fatal(err)
	}
	f.want(f.card, "250.00", 2)
	// Currency must match the balance.
	_, err = f.editBeginning(f.pay, transaction.CreateInput{Amount: "1", Currency: "USD", BalanceUID: string(f.pay.UID),
		SpentAt: "2026-10-01T08:00:00+07:00", AdjustmentDirection: sp("increase")})
	fieldErr(t, err, "balance_uid")
}

// TestBeginningBalanceMovesBack: a transaction dated before a balance's
// beginning balance moves it back to that date and time (create, update,
// bulk, either side of a transfer); later ones leave it alone; and the
// beginning balance cannot be dated after the balance's other transactions.
func TestBeginningBalanceMovesBack(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	at := func(e transaction.Transaction, s string) transaction.Transaction {
		tm, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		e.SpentAt, e.SpentTime = s, tm
		return e
	}
	spent := func(b balance.Balance) string { return f.beginningOf(b).SpentAt }

	// New balances start now (2026-11 in the store clock is real time); this
	// expense is earlier.
	f.st.now = func() time.Time { return time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC) }
	cash := mustBalance(t, balance.Input{Name: "cash", Type: "payment_account", Currency: "THB", Balance: dec("100")})
	bank := mustBalance(t, balance.Input{Name: "bank", Type: "payment_account", Currency: "THB", Balance: dec("100")})
	for _, b := range []*balance.Balance{&cash, &bank} {
		if err := f.st.CreateBalance(ctx, f.userUID, b); err != nil {
			t.Fatal(err)
		}
	}
	if s := spent(cash); s != "2026-11-20T12:00:00Z" {
		t.Fatalf("new: %s", s)
	}
	e := f.create(at(f.tx("expense", "1", "THB", cash, nil), "2026-11-10T08:00:00+07:00"))
	if s := spent(cash); s != "2026-11-10T08:00:00+07:00" {
		t.Errorf("after earlier expense: %s", s)
	}
	if bb := f.beginningOf(cash); bb.Version != 2 || bb.UpdatedAt == nil {
		t.Errorf("moved: v%d updated %v", bb.Version, bb.UpdatedAt)
	}
	// Later: no change.
	f.create(at(f.tx("expense", "1", "THB", cash, nil), "2026-11-15T08:00:00Z"))
	if bb := f.beginningOf(cash); bb.SpentAt != "2026-11-10T08:00:00+07:00" || bb.Version != 2 {
		t.Errorf("after later expense: %s v%d", bb.SpentAt, bb.Version)
	}
	// Update moves it further back.
	if _, err := f.st.UpdateWith(ctx, f.userUID, e.ID, WriteOptions{}, func(cur transaction.Transaction) (transaction.Transaction, error) {
		return at(cur, "2026-11-01T00:00:00Z"), nil
	}); err != nil {
		t.Fatal(err)
	}
	if s := spent(cash); s != "2026-11-01T00:00:00Z" {
		t.Errorf("after update: %s", s)
	}
	// A transfer moves both sides' beginning balances.
	f.create(at(f.tx("transfer", "5", "THB", cash, &bank), "2026-10-31T10:00:00+08:00"))
	if s, b := spent(cash), spent(bank); s != "2026-10-31T10:00:00+08:00" || b != "2026-10-31T10:00:00+08:00" {
		t.Errorf("after transfer: cash %s bank %s", s, b)
	}
	// Bulk.
	if out, err := f.st.CreateBulk(ctx, f.userUID, []BulkItem{{Tx: at(f.tx("expense", "1", "THB", bank, nil), "2026-10-01T00:00:00Z")}}, BulkOptions{Atomic: true}); err != nil || !out.Committed {
		t.Fatalf("bulk: %+v %v", out, err)
	}
	if s := spent(bank); s != "2026-10-01T00:00:00Z" {
		t.Errorf("after bulk: %s", s)
	}
	// Balances only move by their transactions, never by these date changes.
	f.want(cash, "93.00", 4) // the date-only update did not write it
	f.want(bank, "104.00", 3)

	// The beginning balance cannot be dated after the earliest other
	// transaction (the transfer, 2026-10-31T02:00Z); at or before it is fine.
	edit := func(s string) error {
		_, err := f.editBeginning(cash, transaction.CreateInput{Amount: "100", Currency: "THB", BalanceUID: string(cash.UID),
			SpentAt: s, AdjustmentDirection: sp("increase")})
		return err
	}
	if msg := fieldErr(t, edit("2026-10-31T02:00:01Z"), "spent_at"); msg == "" {
		t.Error("later than the transfer accepted")
	}
	if err := edit("2026-10-31T02:00:00Z"); err != nil {
		t.Errorf("same time: %v", err)
	}
	if err := edit("2020-01-01T00:00:00Z"); err != nil {
		t.Errorf("earlier: %v", err)
	}
}

// TestBeginningBalanceCurrencyChange: a balance whose only transaction is
// its beginning balance may still change currency; the beginning balance
// follows (new currency and amount). Any other transaction blocks it.
func TestBeginningBalanceCurrencyChange(t *testing.T) {
	f := newFixture(t)
	b, err := f.edit(f.gold, WriteOptions{}, func(in *balance.Input) { in.Currency, in.Balance = "JPY", dec("70000") })
	if err != nil || b.Currency != "JPY" {
		t.Fatalf("currency change: %+v %v", b, err)
	}
	if bb := f.beginningOf(f.gold); bb.Currency != "JPY" || bb.Amount != "70000" || dirOf(bb) != "increase" || bb.Version != 2 {
		t.Errorf("beginning balance: %+v", bb)
	}
	if adj, _ := f.st.List(context.Background(), f.userUID, ListFilter{Type: transaction.BalanceAdjustment}); len(adj) != 0 {
		t.Errorf("adjustments: %d", len(adj))
	}
	f.create(f.tx("expense", "5", "THB", f.pay, nil))
	if _, err := f.edit(f.pay, WriteOptions{}, func(in *balance.Input) { in.Currency = "USD" }); !errors.Is(err, ErrBalanceInUse) {
		t.Errorf("in use: %v", err)
	}
}

// TestMigrateV8ToV9: every existing balance gets a beginning balance worth
// its current amount minus what its transactions did, dated at its earliest
// transaction (or its creation), and the schema matches a fresh one.
func TestMigrateV8ToV9(t *testing.T) {
	url := testURL(t)
	buildV1(t, url)
	db := rawDB(t, url)
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations[1:8] {
		if err := m(ctx, tx); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO schema_migrations (version) VALUES (2), (3), (4), (5), (6), (7), (8)`); err != nil {
		t.Fatal(err)
	}
	const (
		owner = "00000000-0000-4000-8000-000000000001"
		cash  = "00000000-0000-4000-8000-0000000000b1"
		card  = "00000000-0000-4000-8000-0000000000b2"
		loan  = "00000000-0000-4000-8000-0000000000b3"
		empty = "00000000-0000-4000-8000-0000000000b4"
	)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO users (uid, display_name, created_at) VALUES ($1, 'Owner', now())`, owner)
	exec(`INSERT INTO balances (uid, user_uid, name, type, currency, amount_scale, balance_minor, debt_minor, limit_minor, created_at) VALUES
		($2, $1, 'Cash', 'payment_account', 'THB', 2, 9000, NULL, NULL, '2026-10-05 00:00:00+00'),
		($3, $1, 'Card', 'credit_card', 'THB', 2, NULL, 5000, 100000, '2026-10-05 00:00:00+00'),
		($4, $1, 'Loan', 'other_liability', 'THB', 2, NULL, -2000, 100000, '2026-10-05 00:00:00+00'),
		($5, $1, 'Empty', 'payment_account', 'THB', 2, 0, NULL, NULL, '2026-09-01 10:00:00.5+00')`, owner, cash, card, loan, empty)
	n := 0
	ins := func(typ string, amount int64, from string, to any, spent string, extra string) {
		t.Helper()
		n++
		tm, err := time.Parse(time.RFC3339, spent)
		if err != nil {
			t.Fatal(err)
		}
		cols, vals := "", ""
		if col, val, ok := strings.Cut(extra, "="); ok {
			cols, vals = ", "+col, ", "+val
		}
		exec(`INSERT INTO transactions (uid, user_uid, type, amount_minor, amount_scale, currency, balance_uid, account, to_balance_uid, to_account, spent_at, spent_at_utc, created_at, balance_applied`+cols+`)
			VALUES (gen_random_uuid(), $1, $2, $3, 2, 'THB', $4, 'x', $5, CASE WHEN $5::uuid IS NULL THEN NULL ELSE 'y' END, $6, $7, now(), true`+vals+`)`,
			owner, typ, amount, from, to, spent, tm.UTC())
	}
	ins("expense", 1000, cash, nil, "2026-10-02T08:00:00Z", "")
	ins("income", 500, cash, nil, "2026-10-01T08:00:00+07:00", "") // earliest for Cash
	ins("transfer", 2000, cash, card, "2026-10-03T08:00:00Z", "")
	ins("balance_adjustment", 300, cash, nil, "2026-10-04T08:00:00Z", "adjustment_direction='decrease'")
	ins("repay_debt", 1000, cash, loan, "2026-10-04T09:00:00Z", "interest_amount_minor=100")
	// Cash: value -1000 +500 -2000 -1100, amount -300: started at 90.00 + 3600 + 300 = 129.00.
	// Card: debt 50.00, the transfer paid 20.00 of it: started at 70.00.
	// Loan: debt -20.00 (in credit) after a 10.00 repayment: started at -10.00.

	st, err := Open(url)
	if err != nil {
		t.Fatalf("open v8: %v", err)
	}
	defer st.Close()
	if v := schemaVersion(t, url); v != SchemaVersion {
		t.Errorf("schema version after upgrade = %d, want %d", v, SchemaVersion)
	}
	list, err := st.List(ctx, owner, ListFilter{Type: transaction.BeginningBalance})
	if err != nil || len(list) != 4 {
		t.Fatalf("beginning balances: %d %v", len(list), err)
	}
	got := map[string]string{}
	for _, e := range list {
		got[string(e.BalanceUID)] = e.Amount + " " + dirOf(e) + " " + e.SpentAt + " " + e.Account
		if e.Version != 1 || !e.AdjustsBalances || e.Currency != "THB" {
			t.Errorf("row: %+v", e)
		}
	}
	for uid, want := range map[string]string{
		cash:  "129.00 increase 2026-10-01T08:00:00+07:00 Cash",
		card:  "70.00 increase 2026-10-03T08:00:00Z Card",
		loan:  "10.00 decrease 2026-10-04T09:00:00Z Loan",
		empty: "0.00 increase 2026-09-01T10:00:00Z Empty",
	} {
		if got[uid] != want {
			t.Errorf("%s: %q, want %q", uid, got[uid], want)
		}
	}
	// Balances are unchanged.
	b, _ := st.GetBalanceByUID(ctx, owner, cash)
	if *b.Balance != "90.00" {
		t.Errorf("cash: %s", *b.Balance)
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
