package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
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
	url := testURL(t)
	st, err := Open(url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	owner := seedOwner(t, st)
	card := mustBalance(t, balance.Input{Name: "UOB One", Type: "credit_card", Currency: "SGD", Debt: dec("1200"), Limit: dec("1000")})
	if err := st.CreateBalance(ctx, owner, &card); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetBalance(ctx, owner, card.ID)
	if *got.Debt != "1200.00" || *got.Limit != "1000.00" || *got.Available != "-200.00" || !*got.OverLimit || got.Balance != nil {
		t.Errorf("credit card read back: %+v", got)
	}

	// Case-insensitive unique name.
	dup := mustBalance(t, balance.Input{Name: "uob one", Type: "other_asset", Currency: "SGD", Balance: dec("1")})
	if err := st.CreateBalance(ctx, owner, &dup); !errors.Is(err, ErrDuplicateName) {
		t.Errorf("duplicate name: got %v", err)
	}

	cash := mustBalance(t, balance.Input{Name: "Cash", Type: "payment_account", Currency: "THB", Balance: dec("-35")})
	if err := st.CreateBalance(ctx, owner, &cash); err != nil {
		t.Fatal(err)
	}
	// Renaming onto an existing name (different case) is rejected too.
	if _, err := st.UpdateBalance(ctx, owner, cash.ID, func(cur balance.Balance) (balance.Balance, error) {
		in := cur.Input()
		in.Name = "UOB ONE"
		return in.Validate()
	}); !errors.Is(err, ErrDuplicateName) {
		t.Errorf("rename to duplicate: got %v", err)
	}
	// The type is immutable: even a callback that returns another type is refused
	// and nothing is written.
	if _, err := st.UpdateBalance(ctx, owner, cash.ID, func(cur balance.Balance) (balance.Balance, error) {
		return balance.Input{Name: "Cash", Type: "other_liability", Currency: "THB", Debt: dec("10"), Limit: dec("0")}.Validate()
	}); err == nil {
		t.Errorf("type change via store: expected error")
	}
	upd, err := st.UpdateBalance(ctx, owner, cash.ID, func(cur balance.Balance) (balance.Balance, error) {
		in := cur.Input()
		in.Balance = dec("12")
		return in.ValidateUpdate(cur.Type)
	})
	if err != nil || *upd.Balance != "12.00" || upd.Type != balance.TypePaymentAccount || upd.UpdatedAt == nil {
		t.Errorf("same-type update: %+v %v", upd, err)
	}

	list, _ := st.ListBalances(ctx, owner, "")
	if len(list) != 2 || list[0].Type != balance.TypePaymentAccount || list[1].Type != balance.TypeCreditCard {
		t.Errorf("list order: %+v", list)
	}
	if only, _ := st.ListBalances(ctx, owner, balance.TypeCreditCard); len(only) != 1 {
		t.Errorf("type filter: %d", len(only))
	}
	if err := st.DeleteBalance(ctx, owner, card.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteBalance(ctx, owner, card.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
	if _, err := st.GetBalance(ctx, owner, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("get missing: %v", err)
	}

	// The CHECK constraints are a backstop below validation.
	db := rawDB(t, url)
	for name, q := range map[string]string{
		"asset with debt":    `INSERT INTO balances (uid,user_uid,name,type,currency,amount_scale,balance_minor,debt_minor,created_at) VALUES ('aaaaaaaa-0000-4000-8000-000000000001','` + owner + `','x','payment_account','THB',2,1,1,'2026-01-01T00:00:00Z')`,
		"liability no limit": `INSERT INTO balances (uid,user_uid,name,type,currency,amount_scale,debt_minor,created_at) VALUES ('aaaaaaaa-0000-4000-8000-000000000002','` + owner + `','y','credit_card','THB',2,1,'2026-01-01T00:00:00Z')`,
		"negative limit":     `INSERT INTO balances (uid,user_uid,name,type,currency,amount_scale,debt_minor,limit_minor,created_at) VALUES ('aaaaaaaa-0000-4000-8000-000000000003','` + owner + `','z','credit_card','THB',2,1,-5,'2026-01-01T00:00:00Z')`,
		"bad type":           `INSERT INTO balances (uid,user_uid,name,type,currency,amount_scale,balance_minor,created_at) VALUES ('aaaaaaaa-0000-4000-8000-000000000004','` + owner + `','w','wallet','THB',2,1,'2026-01-01T00:00:00Z')`,
		"version 0":          `INSERT INTO balances (uid,user_uid,name,type,currency,amount_scale,balance_minor,created_at,version) VALUES ('aaaaaaaa-0000-4000-8000-000000000005','` + owner + `','v','payment_account','THB',2,1,'2026-01-01T00:00:00Z',0)`,
	} {
		if _, err := db.Exec(q); err == nil || !strings.Contains(err.Error(), "check constraint") {
			t.Errorf("CHECK constraint did not reject %s: %v", name, err)
		}
	}
	// A negative debt (overpaid card, in credit) is allowed.
	if _, err := db.Exec(`INSERT INTO balances (uid,user_uid,name,type,currency,amount_scale,debt_minor,limit_minor,created_at) VALUES ('aaaaaaaa-0000-4000-8000-000000000006','` + owner + `','neg','credit_card','THB',2,-100,500,'2026-01-01T00:00:00Z')`); err != nil {
		t.Errorf("negative debt rejected: %v", err)
	}
}

func TestBalanceUIDAssignedAndImmutable(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	owner := seedOwner(t, st)
	b := mustBalance(t, balance.Input{Name: "Cash", Type: "payment_account", Currency: "THB", Balance: dec("10")})
	b.UID = "client-supplied-should-be-ignored"
	if err := st.CreateBalance(ctx, owner, &b); err != nil {
		t.Fatal(err)
	}
	if !isUUIDv4(string(b.UID)) || b.UID == "client-supplied-should-be-ignored" {
		t.Fatalf("uid = %q", b.UID)
	}
	orig := b.UID

	upd, err := st.UpdateBalance(ctx, owner, b.ID, func(cur balance.Balance) (balance.Balance, error) {
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
	got, err := st.GetBalanceByUID(ctx, owner, orig)
	if err != nil || got.ID != b.ID {
		t.Errorf("GetBalanceByUID: %+v %v", got, err)
	}
	if _, err := st.GetBalanceByUID(ctx, owner, "00000000-0000-4000-8000-000000000099"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing uid: %v", err)
	}
}

// isUUIDv4 reports whether v is a string holding a canonical lowercase
// RFC 4122 version-4 UUID, as generated by common_type.NewUID.
func isUUIDv4(v any) bool {
	s, _ := v.(string)
	id, err := uuid.Parse(s)
	return err == nil && id.Version() == uuidVersion4 && id.Variant() == uuid.RFC4122 && id.String() == s
}

const uuidVersion4 = 4
