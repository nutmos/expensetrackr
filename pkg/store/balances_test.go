package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/nutmos/expensetrackr/pkg/balance"
	"github.com/nutmos/expensetrackr/pkg/money"
)

func mustBalance(t *testing.T, in balance.Input) balance.Balance {
	t.Helper()
	b, err := in.Validate()
	if err != nil {
		t.Fatalf("validate %+v: %v", in, err)
	}
	return b
}

func dec(s string) *money.DecimalInput { d := money.DecimalInput(s); return &d }

func TestBalanceCRUDAndConstraints(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "b.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	card := mustBalance(t, balance.Input{Name: "UOB One", Type: "credit_card", Currency: "SGD", Debt: dec("1200"), Limit: dec("1000")})
	if err := st.CreateBalance(ctx, &card); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetBalance(ctx, card.ID)
	if *got.Debt != "1200.00" || *got.Limit != "1000.00" || *got.Available != "-200.00" || !*got.OverLimit || got.Balance != nil {
		t.Errorf("credit card read back: %+v", got)
	}

	// Case-insensitive unique name.
	dup := mustBalance(t, balance.Input{Name: "uob one", Type: "other_asset", Currency: "SGD", Balance: dec("1")})
	if err := st.CreateBalance(ctx, &dup); !errors.Is(err, ErrDuplicateName) {
		t.Errorf("duplicate name: got %v", err)
	}

	cash := mustBalance(t, balance.Input{Name: "Cash", Type: "payment_account", Currency: "THB", Balance: dec("-35")})
	if err := st.CreateBalance(ctx, &cash); err != nil {
		t.Fatal(err)
	}
	// Renaming onto an existing name (different case) is rejected too.
	if _, err := st.UpdateBalance(ctx, cash.ID, func(cur balance.Balance) (balance.Balance, error) {
		in := cur.Input()
		in.Name = "UOB ONE"
		return in.Validate()
	}); !errors.Is(err, ErrDuplicateName) {
		t.Errorf("rename to duplicate: got %v", err)
	}
	// The type is immutable: even a callback that returns another type is refused
	// and nothing is written.
	if _, err := st.UpdateBalance(ctx, cash.ID, func(cur balance.Balance) (balance.Balance, error) {
		return balance.Input{Name: "Cash", Type: "other_liability", Currency: "THB", Debt: dec("10"), Limit: dec("0")}.Validate()
	}); err == nil {
		t.Errorf("type change via store: expected error")
	}
	upd, err := st.UpdateBalance(ctx, cash.ID, func(cur balance.Balance) (balance.Balance, error) {
		in := cur.Input()
		in.Balance = dec("12")
		return in.ValidateUpdate(cur.Type)
	})
	if err != nil || *upd.Balance != "12.00" || upd.Type != balance.TypePaymentAccount || upd.UpdatedAt == nil {
		t.Errorf("same-type update: %+v %v", upd, err)
	}

	list, _ := st.ListBalances(ctx, "")
	if len(list) != 2 || list[0].Type != balance.TypePaymentAccount || list[1].Type != balance.TypeCreditCard {
		t.Errorf("list order: %+v", list)
	}
	if only, _ := st.ListBalances(ctx, balance.TypeCreditCard); len(only) != 1 {
		t.Errorf("type filter: %d", len(only))
	}
	if err := st.DeleteBalance(ctx, card.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteBalance(ctx, card.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
	if _, err := st.GetBalance(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("get missing: %v", err)
	}

	// The CHECK constraints are a backstop below validation.
	db, _ := sql.Open("sqlite", "file:"+path)
	defer db.Close()
	for name, q := range map[string]string{
		"asset with debt":    `INSERT INTO balances (uid,name,type,currency,amount_scale,balance_minor,debt_minor,created_at) VALUES ('aaaaaaaa-0000-4000-8000-000000000001','x','payment_account','THB',2,1,1,'t')`,
		"liability no limit": `INSERT INTO balances (uid,name,type,currency,amount_scale,debt_minor,created_at) VALUES ('aaaaaaaa-0000-4000-8000-000000000002','y','credit_card','THB',2,1,'t')`,
		"negative limit":     `INSERT INTO balances (uid,name,type,currency,amount_scale,debt_minor,limit_minor,created_at) VALUES ('aaaaaaaa-0000-4000-8000-000000000003','z','credit_card','THB',2,1,-5,'t')`,
		"bad type":           `INSERT INTO balances (uid,name,type,currency,amount_scale,balance_minor,created_at) VALUES ('aaaaaaaa-0000-4000-8000-000000000004','w','wallet','THB',2,1,'t')`,
		"version 0":          `INSERT INTO balances (uid,name,type,currency,amount_scale,balance_minor,created_at,version) VALUES ('aaaaaaaa-0000-4000-8000-000000000005','v','payment_account','THB',2,1,'t',0)`,
	} {
		if _, err := db.Exec(q); err == nil {
			t.Errorf("CHECK constraint did not reject %s", name)
		}
	}
	// A negative debt (overpaid card, in credit) is allowed.
	if _, err := db.Exec(`INSERT INTO balances (uid,name,type,currency,amount_scale,debt_minor,limit_minor,created_at) VALUES ('aaaaaaaa-0000-4000-8000-000000000006','neg','credit_card','THB',2,-100,500,'t')`); err != nil {
		t.Errorf("negative debt rejected: %v", err)
	}
}

func looksLikeUUID(s string) bool {
	// UUID v4 canonical form: 8-4-4-4-12 hex with version nibble 4.
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		case 14:
			if c != '4' {
				return false
			}
		default:
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
				return false
			}
		}
	}
	return true
}

func TestBalanceUIDAssignedAndImmutable(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "uid.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	b := mustBalance(t, balance.Input{Name: "Cash", Type: "payment_account", Currency: "THB", Balance: dec("10")})
	b.UID = "client-supplied-should-be-ignored"
	if err := st.CreateBalance(ctx, &b); err != nil {
		t.Fatal(err)
	}
	if !looksLikeUUID(string(b.UID)) || b.UID == "client-supplied-should-be-ignored" {
		t.Fatalf("uid = %q", b.UID)
	}
	orig := b.UID

	upd, err := st.UpdateBalance(ctx, b.ID, func(cur balance.Balance) (balance.Balance, error) {
		in := cur.Input()
		in.Balance = dec("20")
		in.UID = "00000000-0000-4000-8000-000000000000"
		return in.Validate()
	})
	if err != nil {
		t.Fatal(err)
	}
	if upd.UID != orig || *upd.Balance != "20.00" {
		t.Errorf("update changed uid or amount: %+v", upd)
	}
	got, err := st.GetBalanceByUID(ctx, string(orig))
	if err != nil || got.ID != b.ID {
		t.Errorf("GetBalanceByUID: %+v %v", got, err)
	}
	if _, err := st.GetBalanceByUID(ctx, "00000000-0000-4000-8000-000000000099"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing uid: %v", err)
	}
}
