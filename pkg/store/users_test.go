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
		Preferences: json.RawMessage(`{ "base_currency": "thb" }`)})
	if !isUUIDv4(a.UID) || *a.Username != "alice" || *a.Email != "alice@example.com" ||
		a.Preferences.BaseCurrency != "THB" || a.Status != user.StatusActive || a.HasPassword {
		t.Fatalf("created: %+v", a)
	}
	got, err := st.GetUserByUID(ctx, a.UID)
	if err != nil || got.Preferences != a.Preferences || got.PasswordHash != nil {
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
	for _, raw := range []string{`[1,2]`, `{bad`} {
		if _, err := st.db.ExecContext(ctx, `INSERT INTO users (uid, display_name, preferences, status, created_at)
			VALUES ('00000000-0000-4000-8000-000000000001', 'x', ?, 'active', '2026-01-01T00:00:00Z')`, raw); err == nil {
			t.Errorf("preferences %s accepted by DB", raw)
		}
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
	// The only user with a password keeps it (issue #77); once another
	// user has one, it can be removed.
	if err := st.SetPasswordHash(ctx, a.UID, nil); !errors.Is(err, ErrLastPasswordUser) {
		t.Fatalf("remove last password: %v, want ErrLastPasswordUser", err)
	}
	if got, _ := st.GetUserByUID(ctx, a.UID); !got.HasPassword {
		t.Errorf("last password was removed: %+v", got)
	}
	admin2 := user.User{Username: sp("other-admin"), DisplayName: "Other"}
	if err := st.RegisterUser(ctx, &admin2, "$2a$04$abcdefghijklmnopqrstuuJ3nL0bS6xkBVb0iH8B3lXz9P2vS5V3a", false); err != nil {
		t.Fatal(err)
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
	if err := st.CreateIdentity(ctx, &g); err != nil || !isUUIDv4(g.UID) || g.ProviderSubject != "1234567890" {
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
	if us, _ := st.ListUsers(ctx); len(us) != 4 { // incl. admin2, the remaining login user
		t.Errorf("remaining users: %d", len(us))
	}
}
