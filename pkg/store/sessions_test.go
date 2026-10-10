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

// downgradeToV13 turns a fresh database into a v13 one (no sessions table).
func downgradeToV13(t *testing.T, path string, dropSessions bool) {
	t.Helper()
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	script := `INSERT INTO users (uid, username, display_name, created_at) VALUES ('11111111-1111-4111-8111-111111111111', 'keep', 'Keep Me', '2026-01-01T00:00:00Z');
INSERT INTO categories (uid, name, type, description, created_at) VALUES ('22222222-2222-4222-8222-222222222222', 'Food', 'expense', '', '2026-01-01T00:00:00Z');
PRAGMA user_version = 13;`
	if dropSessions {
		script = "DROP TABLE sessions;\n" + script
	}
	rawDB(t, path, script)
}

func TestMigrationV13ToV14AddsSessions(t *testing.T) {
	for _, drop := range []bool{true, false} { // false: tables already exist (idempotent)
		path := filepath.Join(t.TempDir(), "v13.db")
		downgradeToV13(t, path, drop)
		st, err := Open(path)
		if err != nil {
			t.Fatalf("open v13 (drop=%v): %v", drop, err)
		}
		u, err := st.GetUserByUsername(context.Background(), "KEEP")
		if err != nil || u.DisplayName != "Keep Me" || u.HasPassword {
			t.Errorf("user not preserved: %+v %v", u, err)
		}
		st.Close()
		assertCurrentSchema(t, path)
	}
}

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
