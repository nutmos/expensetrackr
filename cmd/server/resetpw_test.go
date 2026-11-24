package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nutmos/expensetrackr/pkg/auth"
	"github.com/nutmos/expensetrackr/pkg/store"
	"github.com/nutmos/expensetrackr/pkg/store/storetest"
	"github.com/nutmos/expensetrackr/pkg/user"
)

// resetFixture is a test database with user "alice" (password "old-password")
// who has one session.
func resetFixture(t *testing.T) (url string, st *store.Store, alice user.User) {
	t.Helper()
	old := auth.BcryptCost
	auth.BcryptCost = 4
	t.Cleanup(func() { auth.BcryptCost = old })
	url = storetest.URL(t)
	st, err := store.Open(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	name := "alice"
	alice = user.User{Username: &name, DisplayName: "Alice"}
	hash, _ := auth.HashPassword("old-password")
	if err := st.RegisterUser(ctx, &alice, hash, true); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := st.CreateSession(ctx, &store.Session{TokenHash: strings.Repeat("a", 64), UserUID: alice.UID,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour), LastSeenAt: now}); err != nil {
		t.Fatal(err)
	}
	return url, st, alice
}

func runReset(t *testing.T, stdin string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = runResetPassword(args, strings.NewReader(stdin), &out, &errb)
	return code, out.String(), errb.String()
}

func currentHash(t *testing.T, st *store.Store, uid string) string {
	t.Helper()
	u, err := st.GetUserByUID(context.Background(), uid)
	if err != nil || u.PasswordHash == nil {
		t.Fatalf("get user: %+v %v", u, err)
	}
	return *u.PasswordHash
}

func TestResetPasswordGenerated(t *testing.T) {
	url, st, alice := resetFixture(t)
	code, out, errOut := runReset(t, "", "-database-url", url, "  Alice ")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	pw := strings.TrimSpace(out)
	if len(pw) < 20 || strings.Contains(out, "reset") {
		t.Fatalf("stdout should be only the new password, got %q", out)
	}
	h := currentHash(t, st, alice.UID)
	if !auth.CheckPassword(h, pw) || auth.CheckPassword(h, "old-password") {
		t.Error("printed password does not replace the old one")
	}
	if _, err := st.GetSession(context.Background(), strings.Repeat("a", 64)); err == nil {
		t.Error("session survived the reset")
	}
}

func TestResetPasswordFromStdin(t *testing.T) {
	url, st, alice := resetFixture(t)
	code, out, errOut := runReset(t, "brand-new-pass\r\nignored\n", "-database-url", url, "-password-stdin", "alice")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if out != "" {
		t.Errorf("a chosen password must not be echoed, stdout %q", out)
	}
	if !auth.CheckPassword(currentHash(t, st, alice.UID), "brand-new-pass") {
		t.Error("password from stdin not set")
	}
}

func TestResetPasswordErrors(t *testing.T) {
	url, st, alice := resetFixture(t)
	before := currentHash(t, st, alice.UID)
	for _, tc := range []struct {
		name, stdin string
		args        []string
		code        int
		msg         string
	}{
		{"no username", "", []string{"-database-url", url}, 2, "exactly one USERNAME"},
		{"two usernames", "", []string{"-database-url", url, "alice", "bob"}, 2, "exactly one USERNAME"},
		{"no database", "", []string{"-database-url", "", "alice"}, 2, "no database"},
		{"unknown user", "", []string{"-database-url", url, "nobody"}, 1, `no user "nobody"`},
		{"short password", "short\n", []string{"-database-url", url, "-password-stdin", "alice"}, 2, "at least 8"},
		{"empty stdin", "", []string{"-database-url", url, "-password-stdin", "alice"}, 2, "at least 8"},
		{"bad flag", "", []string{"-nope", "alice"}, 2, "flag provided but not defined"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errOut := runReset(t, tc.stdin, tc.args...)
			if code != tc.code || !strings.Contains(errOut, tc.msg) || out != "" {
				t.Errorf("exit %d, stdout %q, stderr %q; want exit %d with %q", code, out, errOut, tc.code, tc.msg)
			}
		})
	}
	if currentHash(t, st, alice.UID) != before {
		t.Error("a failed run changed the password")
	}
}
