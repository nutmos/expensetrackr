package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nutmos/expensetrackr/pkg/balance"
	"github.com/nutmos/expensetrackr/pkg/category"
	"github.com/nutmos/expensetrackr/pkg/fieldcrypt"
	"github.com/nutmos/expensetrackr/pkg/transaction"
	"github.com/nutmos/expensetrackr/pkg/user"
)

func newTestKeyring(t *testing.T, old ...string) (*fieldcrypt.Keyring, string) {
	t.Helper()
	k, err := fieldcrypt.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	r, err := fieldcrypt.ParseKeyring(k, strings.Join(old, ","))
	if err != nil {
		t.Fatal(err)
	}
	return r, k
}

// personalData writes one of each encrypted value through st and returns
// the owner's uid and the plain texts written.
func personalData(t *testing.T, st *Store) (string, []string) {
	t.Helper()
	ctx := context.Background()
	name := "olivia"
	u := user.User{Username: &name, DisplayName: "Olivia Secretname"}
	if err := st.RegisterUser(ctx, &u, "$2a$04$unused", false); err != nil {
		t.Fatal(err)
	}
	b := mustBalance(t, balance.Input{Name: "Hidden Bank Account", Type: "payment_account", Currency: "THB", Balance: dec("100"), Description: "joint with Bob"})
	if err := st.CreateBalance(ctx, u.UID, &b); err != nil {
		t.Fatal(err)
	}
	c := category.Category{Name: "Therapy sessions", Type: category.TypeExpense}
	if err := st.CreateCategory(ctx, u.UID, &c); err != nil {
		t.Fatal(err)
	}
	e, err := transaction.CreateInput{Amount: "5", Currency: "THB", BalanceUID: string(b.UID), CategoryUID: string(c.UID), SpentAt: "2026-10-07T08:00:00+07:00", Note: "Dr. Confidential"}.Validate()
	if err != nil {
		t.Fatal(err)
	}
	e.Account = b.Name
	if err := st.Create(ctx, u.UID, &e); err != nil {
		t.Fatal(err)
	}
	return u.UID, []string{"Olivia Secretname", "Hidden Bank Account", "joint with Bob", "Therapy sessions", "Dr. Confidential"}
}

// rawDump is every encrypted column's stored text, for checking what a
// reader of the database sees.
func rawDump(t *testing.T, url string) string {
	t.Helper()
	db := rawDB(t, url)
	var parts []string
	for _, q := range []string{
		`SELECT string_agg(note || '|' || account || '|' || coalesce(to_account, ''), '|') FROM transactions`,
		`SELECT string_agg(name || '|' || description, '|') FROM balances`,
		`SELECT string_agg(display_name, '|') FROM users`,
		`SELECT coalesce(string_agg(categories::text, '|'), '') FROM user_categories`,
	} {
		var s string
		if err := db.QueryRow(q).Scan(&s); err != nil {
			t.Fatal(err)
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "\n")
}

// checkReadable reads the data back through st and checks the plain texts.
func checkReadable(t *testing.T, st *Store, owner string) {
	t.Helper()
	ctx := context.Background()
	u, err := st.GetUserByUID(ctx, owner)
	if err != nil || u.DisplayName != "Olivia Secretname" {
		t.Fatalf("user: %q, %v", u.DisplayName, err)
	}
	bs, err := st.ListBalances(ctx, owner, "")
	if err != nil || len(bs) != 1 || bs[0].Name != "Hidden Bank Account" || bs[0].Description != "joint with Bob" {
		t.Fatalf("balances: %+v, %v", bs, err)
	}
	cs, err := st.ListCategories(ctx, owner, "")
	if err != nil || len(cs) != 1 || cs[0].Name != "Therapy sessions" {
		t.Fatalf("categories: %+v, %v", cs, err)
	}
	txs, err := withoutBeginning(st.List(ctx, owner, ListFilter{}))
	if err != nil || len(txs) != 1 || txs[0].Note != "Dr. Confidential" || txs[0].Account != "Hidden Bank Account" {
		t.Fatalf("transactions: %+v, %v", txs, err)
	}
}

func TestEncryptedAtRest(t *testing.T) {
	url := testURL(t)
	keys, _ := newTestKeyring(t)
	st, err := Open(url, WithKeyring(keys))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	owner, secrets := personalData(t, st)

	dump := rawDump(t, url)
	for _, s := range secrets {
		if strings.Contains(dump, s) {
			t.Errorf("%q is stored in clear:\n%s", s, dump)
		}
	}
	if !strings.Contains(dump, "enc1:"+keys.PrimaryID()+":") {
		t.Errorf("no sealed values in:\n%s", dump)
	}
	checkReadable(t, st, owner)

	// Edits go through the same path.
	ctx := context.Background()
	bs, _ := st.ListBalances(ctx, owner, "")
	if _, err := st.UpdateBalance(ctx, owner, bs[0].ID, func(cur balance.Balance) (balance.Balance, error) {
		cur.Name = "Renamed Secret"
		v := int64(20000)
		cur.BalanceMinor = &v // records a balance adjustment with a note
		return cur, nil
	}); err != nil {
		t.Fatal(err)
	}
	if dump := rawDump(t, url); strings.Contains(dump, "Renamed Secret") || strings.Contains(dump, "Manual edit") {
		t.Errorf("edit stored in clear:\n%s", dump)
	}
	got, err := st.GetBalance(ctx, owner, bs[0].ID)
	if err != nil || got.Name != "Renamed Secret" {
		t.Errorf("renamed balance: %q, %v", got.Name, err)
	}
}

func TestEncryptExistingDatabase(t *testing.T) {
	url := testURL(t)
	plain, err := Open(url) // encryption off: the data is written in clear
	if err != nil {
		t.Fatal(err)
	}
	owner, secrets := personalData(t, plain)
	plain.Close()
	if dump := rawDump(t, url); !strings.Contains(dump, secrets[0]) {
		t.Fatalf("expected clear text before encrypting:\n%s", dump)
	}

	keys, key := newTestKeyring(t)
	st, err := Open(url, WithKeyring(keys))
	if err != nil {
		t.Fatal(err)
	}
	dump := rawDump(t, url)
	for _, s := range secrets {
		if strings.Contains(dump, s) {
			t.Errorf("%q still in clear after encrypting:\n%s", s, dump)
		}
	}
	checkReadable(t, st, owner)
	st.Close()

	// Now the key is required, and must be the right one.
	if _, err := Open(url); !errors.Is(err, ErrEncryptionKeyRequired) {
		t.Errorf("open without key: %v", err)
	}
	wrong, _ := newTestKeyring(t)
	if _, err := Open(url, WithKeyring(wrong)); !errors.Is(err, ErrWrongEncryptionKey) {
		t.Errorf("open with a wrong key: %v", err)
	}
	// Reopening with the key changes nothing.
	again, _ := fieldcrypt.ParseKeyring(key, "")
	st, err = Open(url, WithKeyring(again))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if rawDump(t, url) != dump {
		t.Error("reopening with the same key rewrote values")
	}
	checkReadable(t, st, owner)
}

func TestRotateEncryptionKey(t *testing.T) {
	url := testURL(t)
	oldKeys, oldKey := newTestKeyring(t)
	st, err := Open(url, WithKeyring(oldKeys))
	if err != nil {
		t.Fatal(err)
	}
	owner, _ := personalData(t, st)
	st.Close()

	newKeys, newKey := newTestKeyring(t, oldKey)
	st, err = Open(url, WithKeyring(newKeys))
	if err != nil {
		t.Fatal(err)
	}
	checkReadable(t, st, owner)
	st.Close()
	dump := rawDump(t, url)
	if strings.Contains(dump, "enc1:"+oldKeys.PrimaryID()+":") {
		t.Errorf("values still sealed with the old key:\n%s", dump)
	}

	// The old key is no longer needed; on its own it no longer opens the
	// database.
	only, _ := fieldcrypt.ParseKeyring(newKey, "")
	st, err = Open(url, WithKeyring(only))
	if err != nil {
		t.Fatal(err)
	}
	checkReadable(t, st, owner)
	st.Close()
	if _, err := Open(url, WithKeyring(oldKeys)); !errors.Is(err, ErrWrongEncryptionKey) {
		t.Errorf("open with the retired key: %v", err)
	}
}

func TestListBalancesOrder(t *testing.T) {
	st := openTestStore(t)
	owner := seedOwner(t, st)
	ctx := context.Background()
	for _, in := range []balance.Input{
		{Name: "zeta", Type: "payment_account", Currency: "THB", Balance: dec("1")},
		{Name: "Loan", Type: "other_liability", Currency: "THB", Debt: dec("1"), Limit: dec("1")},
		{Name: "Alpha", Type: "payment_account", Currency: "THB", Balance: dec("1")},
		{Name: "card", Type: "credit_card", Currency: "THB", Debt: dec("1"), Limit: dec("1")},
		{Name: "beta", Type: "payment_account", Currency: "THB", Balance: dec("1")},
		{Name: "Gold", Type: "other_asset", Currency: "THB", Balance: dec("1")},
		{Name: "Beta", Type: "payment_account", Currency: "THB", Balance: dec("1")},
	} {
		b := mustBalance(t, in)
		if err := st.CreateBalance(ctx, owner, &b); err != nil {
			t.Fatal(err)
		}
	}
	bs, err := st.ListBalances(ctx, owner, "")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, b := range bs {
		got = append(got, b.Name)
	}
	// Type first, then name ignoring case, then creation order.
	if want := "Alpha beta Beta zeta card Gold Loan"; strings.Join(got, " ") != want {
		t.Errorf("order = %v, want %s", got, want)
	}
}

func TestMigrateV9ToV10(t *testing.T) {
	url := testURL(t)
	buildV1(t, url)
	db := rawDB(t, url)
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations[1:9] {
		if err := m(ctx, tx); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO schema_migrations (version) VALUES (2), (3), (4), (5), (6), (7), (8), (9)`); err != nil {
		t.Fatal(err)
	}
	const owner = "00000000-0000-4000-8000-000000000001"
	if _, err := db.Exec(`INSERT INTO users (uid, display_name, created_at) VALUES ($1, 'Owner', now())`, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO user_categories (user_uid, categories, created_at) VALUES ($1,
		'[{"uid":"00000000-0000-4000-8000-0000000000c1","name":"Food","type":"expense","description":"","created_at":"2026-01-01T00:00:00Z","updated_at":null}]', now())`, owner); err != nil {
		t.Fatal(err)
	}

	keys, _ := newTestKeyring(t)
	st, err := Open(url, WithKeyring(keys))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if v := schemaVersion(t, url); v != 10 {
		t.Errorf("schema version = %d, want 10", v)
	}
	if def := schemaObjects(t, url)["check user_categories.user_categories_categories_check"]; !strings.Contains(def, "'string'") {
		t.Errorf("categories check = %q", def)
	}
	var typ string
	if err := db.QueryRow(`SELECT jsonb_typeof(categories) FROM user_categories`).Scan(&typ); err != nil || typ != "string" {
		t.Errorf("stored categories type = %q, %v", typ, err)
	}
	cs, err := st.ListCategories(ctx, owner, "")
	if err != nil || len(cs) != 1 || cs[0].Name != "Food" {
		t.Errorf("categories after migrating and encrypting: %+v, %v", cs, err)
	}
	// Anything other than an array or a string is still refused.
	if _, err := db.Exec(`UPDATE user_categories SET categories = '{}'`); err == nil {
		t.Error("a jsonb object was accepted as categories")
	}
}
