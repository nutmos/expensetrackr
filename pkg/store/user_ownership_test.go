package store

import (
	"context"
	"errors"
	"testing"

	"github.com/nutmos/expensetrackr/pkg/balance"
	"github.com/nutmos/expensetrackr/pkg/transaction"
	"github.com/nutmos/expensetrackr/pkg/user"
	"github.com/nutmos/expensetrackr/pkg/validate"
)

// TestPerUserStoreIsolation: user B cannot read or mutate user A's balances or
// transactions, and cannot create a transaction against A's balance.
func TestPerUserStoreIsolation(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	alice := user.User{Username: ptr("alice"), DisplayName: "Alice"}
	bob := user.User{Username: ptr("bob"), DisplayName: "Bob"}
	if err := st.RegisterUser(ctx, &alice, "$2a$04$x", false); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterUser(ctx, &bob, "$2a$04$x", false); err != nil {
		t.Fatal(err)
	}
	aPay := mustBalance(t, balance.Input{Name: "Cash", Type: "payment_account", Currency: "THB", Balance: dec("1000")})
	if err := st.CreateBalance(ctx, alice.UID, &aPay); err != nil {
		t.Fatal(err)
	}
	bPay := mustBalance(t, balance.Input{Name: "Cash", Type: "payment_account", Currency: "THB", Balance: dec("50")})
	if err := st.CreateBalance(ctx, bob.UID, &bPay); err != nil {
		t.Fatal(err) // same name, different user: allowed
	}
	e, err := transaction.CreateInput{Amount: "10", Currency: "THB", BalanceUID: string(aPay.UID), CategoryUID: testCategoryUID, SpentAt: "2026-10-08T12:00:00+08:00"}.Validate()
	if err != nil {
		t.Fatal(err)
	}
	e.Account = "Cash"
	if err := st.Create(ctx, alice.UID, &e); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		fn   func() error
	}{
		{"bob get alice balance", func() error {
			_, err := st.GetBalanceByUID(ctx, bob.UID, aPay.UID)
			return err
		}},
		{"bob get alice tx", func() error {
			_, err := st.GetByUID(ctx, bob.UID, e.UID)
			return err
		}},
		{"bob update alice tx", func() error {
			_, err := st.UpdateWith(ctx, bob.UID, e.ID, WriteOptions{}, func(cur transaction.Transaction) (transaction.Transaction, error) {
				cur.Note = "x"
				return cur, nil
			})
			return err
		}},
		{"bob delete alice tx", func() error {
			return st.DeleteWith(ctx, bob.UID, e.ID, WriteOptions{})
		}},
		{"bob update alice balance", func() error {
			_, err := st.UpdateBalance(ctx, bob.UID, aPay.ID, func(cur balance.Balance) (balance.Balance, error) {
				return cur, nil
			})
			return err
		}},
		{"bob delete alice balance", func() error {
			return st.DeleteBalance(ctx, bob.UID, aPay.ID)
		}},
	}
	for _, c := range cases {
		if err := c.fn(); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: got %v, want ErrNotFound", c.name, err)
		}
	}
	// Create against alice's balance as bob → validation "does not match".
	cross, err := transaction.CreateInput{Amount: "1", Currency: "THB", BalanceUID: string(aPay.UID), CategoryUID: testCategoryUID, SpentAt: "2026-10-08T12:00:00+08:00"}.Validate()
	if err != nil {
		t.Fatal(err)
	}
	cross.Account = "Cash"
	var ve *validate.ValidationError
	if err := st.Create(ctx, bob.UID, &cross); !errors.As(err, &ve) || ve.Fields["balance_uid"] == "" {
		t.Fatalf("cross-user create: %v", err)
	}
	alist, _ := st.List(ctx, alice.UID, ListFilter{})
	blist, _ := st.List(ctx, bob.UID, ListFilter{})
	if len(alist) != 1 || len(blist) != 0 {
		t.Errorf("lists alice=%d bob=%d", len(alist), len(blist))
	}
	abal, _ := st.ListBalances(ctx, alice.UID, "")
	bbal, _ := st.ListBalances(ctx, bob.UID, "")
	if len(abal) != 1 || len(bbal) != 1 {
		t.Errorf("balances alice=%d bob=%d", len(abal), len(bbal))
	}
}

func ptr(s string) *string { return &s }
