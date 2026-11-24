package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nutmos/expensetrackr/pkg/user"
)

func strp(s string) *string { return &s }

func TestRegisterAndSessions(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	if ok, _ := st.HasPasswordUser(ctx); ok {
		t.Fatal("fresh db has password user")
	}
	u := user.User{Username: strp("alice"), DisplayName: "Alice"}
	if err := st.RegisterUser(ctx, &u, "$2a$04$hash", true); err != nil {
		t.Fatal(err)
	}
	if ok, _ := st.HasPasswordUser(ctx); !ok {
		t.Fatal("no password user after register")
	}
	u2 := user.User{Username: strp("bob"), DisplayName: "Bob"}
	if err := st.RegisterUser(ctx, &u2, "$2a$04$hash", true); !errors.Is(err, ErrRegistrationClosed) {
		t.Fatalf("second first-run register: %v", err)
	}
	u3 := user.User{Username: strp("alice"), DisplayName: "A2"}
	if err := st.RegisterUser(ctx, &u3, "$2a$04$hash", false); !errors.Is(err, ErrDuplicateUsername) {
		t.Fatalf("duplicate: %v", err)
	}

	now := time.Now()
	mk := func(h string) {
		if err := st.CreateSession(ctx, &Session{TokenHash: h, UserUID: u.UID, CreatedAt: now, ExpiresAt: now.Add(time.Hour), LastSeenAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	a, b := strings.Repeat("a", 64), strings.Repeat("b", 64)
	mk(a)
	mk(b)
	if se, err := st.GetSession(ctx, a); err != nil || se.UserUID != u.UID {
		t.Fatalf("get session: %+v %v", se, err)
	}
	if err := st.ChangePassword(ctx, u.UID, "$2a$04$new", a); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetSession(ctx, b); !errors.Is(err, ErrNotFound) {
		t.Error("other session survived password change")
	}
	if _, err := st.GetSession(ctx, a); err != nil {
		t.Error("current session removed by password change")
	}
	if err := st.DeleteExpiredSessions(ctx, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetSession(ctx, a); !errors.Is(err, ErrNotFound) {
		t.Error("expired session not pruned")
	}
}

// TestResetPassword: the offline reset replaces the hash and ends every
// session of that user (none is kept), but not other users' sessions.
func TestResetPassword(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	u := user.User{Username: strp("alice"), DisplayName: "Alice"}
	if err := st.RegisterUser(ctx, &u, "$2a$04$old", true); err != nil {
		t.Fatal(err)
	}
	other := user.User{Username: strp("bob"), DisplayName: "Bob"}
	if err := st.RegisterUser(ctx, &other, "$2a$04$bob", false); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	a, b, c := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	for h, uid := range map[string]string{a: u.UID, b: u.UID, c: other.UID} {
		if err := st.CreateSession(ctx, &Session{TokenHash: h, UserUID: uid, CreatedAt: now, ExpiresAt: now.Add(time.Hour), LastSeenAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.ResetPassword(ctx, u.UID, "$2a$04$new"); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{a, b} {
		if _, err := st.GetSession(ctx, h); !errors.Is(err, ErrNotFound) {
			t.Errorf("session %.1s… survived the reset: %v", h, err)
		}
	}
	if _, err := st.GetSession(ctx, c); err != nil {
		t.Errorf("other user's session removed: %v", err)
	}
	got, err := st.GetUserByUID(ctx, u.UID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PasswordHash == nil || *got.PasswordHash != "$2a$04$new" || got.PasswordUpdated == nil {
		t.Errorf("hash not replaced: %+v", got)
	}
	if err := st.ResetPassword(ctx, "00000000-0000-4000-8000-000000000000", "$2a$04$x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown uid: %v", err)
	}
	if err := st.ResetPassword(ctx, u.UID, ""); err == nil {
		t.Error("empty hash accepted")
	}
}
