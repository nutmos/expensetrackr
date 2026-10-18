package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nutmos/expensetrackr/pkg/user"
)

func strp(s string) *string { return &s }

func TestRegisterAndSessions(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if ok, _ := st.HasPasswordUser(ctx); ok {
		t.Fatal("fresh db has password user")
	}
	u := user.User{Username: strp("alice"), DisplayName: "Alice", Preferences: []byte("{}")}
	if err := st.RegisterUser(ctx, &u, "$2a$04$hash", true); err != nil {
		t.Fatal(err)
	}
	if ok, _ := st.HasPasswordUser(ctx); !ok {
		t.Fatal("no password user after register")
	}
	u2 := user.User{Username: strp("bob"), DisplayName: "Bob", Preferences: []byte("{}")}
	if err := st.RegisterUser(ctx, &u2, "$2a$04$hash", true); !errors.Is(err, ErrRegistrationClosed) {
		t.Fatalf("second first-run register: %v", err)
	}
	u3 := user.User{Username: strp("alice"), DisplayName: "A2", Preferences: []byte("{}")}
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
