package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nutmos/expensetrackr/pkg/user"
)

func seedTokenUser(t *testing.T, st *Store, name string) user.User {
	t.Helper()
	u := user.User{Username: &name, DisplayName: name}
	if err := st.RegisterUser(context.Background(), &u, "$2a$04$x", false); err != nil {
		t.Fatal(err)
	}
	return u
}

func TestAPITokenLifecycle(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	fixed := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	st.now = func() time.Time { return fixed }
	alice, bob := seedTokenUser(t, st, "alice"), seedTokenUser(t, st, "bob")
	hash := strings.Repeat("a", 64)
	exp := fixed.Add(24 * time.Hour)
	tok := APIToken{UserUID: alice.UID, Name: "Expense agent", Prefix: "ext_abcdefgh", TokenHash: hash, Scopes: []string{"read", "write"}, ExpiresAt: &exp}
	if err := st.CreateAPIToken(ctx, &tok); err != nil {
		t.Fatal(err)
	}
	if tok.UID == "" || !tok.CreatedAt.Equal(fixed) {
		t.Fatalf("created: %+v", tok)
	}
	got, err := st.GetAPITokenByHash(ctx, hash)
	if err != nil || got.UID != tok.UID || !got.HasScope("write") || got.ExpiresAt == nil || !got.ExpiresAt.Equal(exp) || got.LastUsedAt != nil {
		t.Fatalf("get by hash: %+v %v", got, err)
	}
	if err := st.TouchAPIToken(ctx, got.ID, fixed); err != nil {
		t.Fatal(err)
	}
	list, err := st.ListAPITokens(ctx, alice.UID)
	if err != nil || len(list) != 1 || list[0].LastUsedAt == nil {
		t.Fatalf("list: %+v %v", list, err)
	}
	if list, _ := st.ListAPITokens(ctx, bob.UID); len(list) != 0 {
		t.Fatalf("bob sees alice's tokens: %+v", list)
	}

	cases := []struct {
		name, owner, uid string
		want             error
	}{
		{name: "other user", owner: bob.UID, uid: tok.UID, want: ErrTokenNotFound},
		{name: "unknown uid", owner: alice.UID, uid: "00000000-0000-4000-8000-000000000000", want: ErrTokenNotFound},
		{name: "owner", owner: alice.UID, uid: tok.UID, want: nil},
		{name: "already revoked", owner: alice.UID, uid: tok.UID, want: ErrTokenNotFound},
	}
	for _, c := range cases {
		if err := st.RevokeAPIToken(ctx, c.owner, c.uid); !errors.Is(err, c.want) {
			t.Errorf("%s: revoke err = %v, want %v", c.name, err, c.want)
		}
	}
	if got, _ := st.GetAPITokenByHash(ctx, hash); got.RevokedAt == nil {
		t.Error("revoked_at not set")
	}
	if list, _ := st.ListAPITokens(ctx, alice.UID); len(list) != 0 {
		t.Errorf("revoked token still listed: %+v", list)
	}
	if _, err := st.GetAPITokenByHash(ctx, strings.Repeat("b", 64)); !errors.Is(err, ErrTokenNotFound) {
		t.Errorf("unknown hash: %v", err)
	}
}
