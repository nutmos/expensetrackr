package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/nutmos/expensetrackr/pkg/user"
	"github.com/nutmos/expensetrackr/pkg/validate"
)

// schemaV9 is a database as shipped at version 9 (categories, category_uid,
// no users yet), with the two v7 fixture transactions.
const schemaV9 = schemaV7 + categoriesSchema + `
ALTER TABLE transactions ADD COLUMN category_uid TEXT CHECK (category_uid IS NULL OR length(category_uid) = 36);
CREATE INDEX idx_transactions_category_uid ON transactions (category_uid);
PRAGMA user_version = 9;
`

func TestMigrationV9ToV10AddsUsers(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v9.db")
	rawDB(t, path, schemaV9)
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open v9: %v", err)
	}
	if items, err := st.List(ctx, ListFilter{}); err != nil || len(items) != 2 {
		t.Fatalf("existing transactions kept: %d %v", len(items), err)
	}
	if us, err := st.ListUsers(ctx); err != nil || len(us) != 0 {
		t.Fatalf("users empty after migration: %v %v", us, err)
	}
	st.Close()
	assertCurrentSchema(t, path)
	for _, col := range []string{"uid", "username", "email", "email_verified_at", "display_name", "preferences",
		"password_hash", "password_updated_at", "status", "last_login_at", "created_at", "updated_at"} {
		if !hasColumn(t, path, "users", col) {
			t.Errorf("users.%s missing", col)
		}
	}
	for _, col := range []string{"uid", "user_uid", "provider", "provider_subject", "email", "created_at", "last_used_at"} {
		if !hasColumn(t, path, "user_identities", col) {
			t.Errorf("user_identities.%s missing", col)
		}
	}
	// Existing tables gain no user scoping yet.
	if hasColumn(t, path, "transactions", "user_uid") || hasColumn(t, path, "balances", "user_uid") {
		t.Error("user_uid must not be added to existing tables yet")
	}
}

// v10 is idempotent: a v9 database that already has the users tables (e.g.
// created by hand) migrates cleanly.
func TestMigrationV10IdempotentIfTablesExist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v9users.db")
	rawDB(t, path, schemaV9+usersSchema+`PRAGMA user_version = 9;`)
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	st.Close()
	assertCurrentSchema(t, path)
}

func newUser(t *testing.T, st *Store, in user.Input) user.User {
	t.Helper()
	u, err := in.Validate()
	if err != nil {
		t.Fatalf("validate %+v: %v", in, err)
	}
	if err := st.CreateUser(context.Background(), &u); err != nil {
		t.Fatalf("create: %v", err)
	}
	return u
}

func sp(s string) *string { return &s }

func TestUserStore(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "u.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	a := newUser(t, st, user.Input{DisplayName: "Alice", Username: sp("Alice"), Email: sp("Alice@Example.com"),
		Preferences: json.RawMessage(`{ "currency": "THB", "theme": {"dark": true} }`)})
	if !uuidV4.MatchString(a.UID) || *a.Username != "alice" || *a.Email != "alice@example.com" ||
		string(a.Preferences) != `{"currency":"THB","theme":{"dark":true}}` || a.Status != user.StatusActive || a.HasPassword {
		t.Fatalf("created: %+v", a)
	}
	got, err := st.GetUserByUID(ctx, a.UID)
	if err != nil || string(got.Preferences) != string(a.Preferences) || got.PasswordHash != nil {
		t.Fatalf("get: %+v %v", got, err)
	}

	// Uniqueness (case-insensitive via normalization + NOCASE indexes).
	b, _ := user.Input{DisplayName: "Bob", Username: sp("ALICE")}.Validate()
	if err := st.CreateUser(ctx, &b); !errors.Is(err, ErrDuplicateUsername) {
		t.Errorf("dup username: %v", err)
	}
	b, _ = user.Input{DisplayName: "Bob", Email: sp("alice@EXAMPLE.com")}.Validate()
	if err := st.CreateUser(ctx, &b); !errors.Is(err, ErrDuplicateEmail) {
		t.Errorf("dup email: %v", err)
	}
	// Many users without username/email are fine (NULLs are not duplicates).
	newUser(t, st, user.Input{DisplayName: "No login 1"})
	newUser(t, st, user.Input{DisplayName: "No login 2"})

	// The database itself rejects non-object preferences.
	bad := user.User{DisplayName: "x", Preferences: json.RawMessage(`[1,2]`)}
	if err := st.CreateUser(ctx, &bad); err == nil {
		t.Error("array preferences accepted by DB")
	}
	bad = user.User{DisplayName: "x", Preferences: json.RawMessage(`{bad`)}
	if err := st.CreateUser(ctx, &bad); err == nil {
		t.Error("invalid JSON accepted by DB")
	}

	// Password hash: stored, never lost by profile updates; email change
	// clears verification.
	if err := st.SetPasswordHash(ctx, a.UID, sp("$argon2id$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA")); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE users SET email_verified_at = '2026-10-08T00:00:00Z' WHERE uid = ?`, a.UID); err != nil {
		t.Fatal(err)
	}
	upd, err := st.UpdateUser(ctx, a.ID, func(cur user.User) (user.User, error) {
		in := cur.Input()
		in.DisplayName = "Alice A."
		return in.Validate()
	})
	if err != nil || !upd.HasPassword || upd.PasswordHash == nil || upd.PasswordUpdated == nil ||
		upd.EmailVerifiedAt == nil || upd.UpdatedAt == nil || upd.DisplayName != "Alice A." {
		t.Fatalf("update keeps auth fields: %+v %v", upd, err)
	}
	upd, err = st.UpdateUser(ctx, a.ID, func(cur user.User) (user.User, error) {
		in := cur.Input()
		in.Email = sp("alice@new.example.com")
		return in.Validate()
	})
	if err != nil || upd.EmailVerifiedAt != nil || !upd.HasPassword {
		t.Fatalf("email change clears verification: %+v %v", upd, err)
	}
	if err := st.SetPasswordHash(ctx, a.UID, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetUserByUID(ctx, a.UID); got.HasPassword || got.PasswordUpdated != nil {
		t.Errorf("password removed: %+v", got)
	}
	if err := st.SetPasswordHash(ctx, "00000000-0000-4000-8000-000000000000", sp("x")); !errors.Is(err, ErrNotFound) {
		t.Errorf("set hash unknown user: %v", err)
	}

	// Identities.
	g := user.Identity{UserUID: a.UID, Provider: user.ProviderGoogle, ProviderSubject: " 1234567890 ", Email: sp("alice@gmail.com")}
	if err := st.CreateIdentity(ctx, &g); err != nil || !uuidV4.MatchString(g.UID) || g.ProviderSubject != "1234567890" {
		t.Fatalf("create identity: %+v %v", g, err)
	}
	ap := user.Identity{UserUID: a.UID, Provider: user.ProviderApple, ProviderSubject: "1234567890"}
	if err := st.CreateIdentity(ctx, &ap); err != nil {
		t.Fatalf("same subject, other provider: %v", err)
	}
	other := newUser(t, st, user.Input{DisplayName: "Carol"})
	dup := user.Identity{UserUID: other.UID, Provider: user.ProviderGoogle, ProviderSubject: "1234567890"}
	if err := st.CreateIdentity(ctx, &dup); !errors.Is(err, ErrDuplicateIdentity) {
		t.Errorf("duplicate (provider, subject) for another user: %v", err)
	}
	ghost := user.Identity{UserUID: "00000000-0000-4000-8000-000000000000", Provider: user.ProviderGoogle, ProviderSubject: "zzz"}
	if err := st.CreateIdentity(ctx, &ghost); !errors.Is(err, ErrNotFound) {
		t.Errorf("identity for unknown user: %v", err)
	}
	var ve *validate.ValidationError
	if err := st.CreateIdentity(ctx, &user.Identity{UserUID: a.UID, Provider: "github", ProviderSubject: "1"}); !errors.As(err, &ve) {
		t.Errorf("unknown provider: %v", err)
	}
	if ids, err := st.ListIdentities(ctx, a.UID); err != nil || len(ids) != 2 || ids[0].Provider != user.ProviderGoogle || *ids[0].Email != "alice@gmail.com" {
		t.Errorf("list identities: %+v %v", ids, err)
	}
	if u, err := st.FindUserByIdentity(ctx, user.ProviderApple, "1234567890"); err != nil || u.UID != a.UID {
		t.Errorf("find by identity: %+v %v", u, err)
	}
	if _, err := st.FindUserByIdentity(ctx, user.ProviderApple, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("find unknown identity: %v", err)
	}

	// Delete cascades to identities.
	if err := st.DeleteUser(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := st.db.QueryRowContext(ctx, `SELECT count(*) FROM user_identities WHERE user_uid = ?`, a.UID).Scan(&n); err != nil || n != 0 {
		t.Errorf("identities after delete: %d %v", n, err)
	}
	if err := st.DeleteUser(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete twice: %v", err)
	}
	if us, _ := st.ListUsers(ctx); len(us) != 3 {
		t.Errorf("remaining users: %d", len(us))
	}
}
