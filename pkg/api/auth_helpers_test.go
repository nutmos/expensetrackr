package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/nutmos/expensetrackr/pkg/auth"
	"github.com/nutmos/expensetrackr/pkg/store"
	"github.com/nutmos/expensetrackr/pkg/user"
)

func init() { auth.BcryptCost = 4 } // fast hashing in tests

// loggedIn creates a "tester" account with a session directly in the store
// and returns a handler that adds that session cookie to every request that
// has none, so tests not about auth run as a logged-in user.
func loggedIn(t *testing.T, h http.Handler, st *store.Store) http.Handler {
	t.Helper()
	ctx := context.Background()
	hash, err := auth.HashPassword("tester-password")
	if err != nil {
		t.Fatal(err)
	}
	name := "tester"
	u := user.User{Username: &name, DisplayName: "Tester", Preferences: []byte("{}")}
	if err := st.RegisterUser(ctx, &u, hash, true); err != nil {
		t.Fatalf("seed tester: %v", err)
	}
	tok, _ := auth.NewToken()
	now := time.Now()
	if err := st.CreateSession(ctx, &store.Session{TokenHash: auth.HashToken(tok), UserUID: u.UID, CreatedAt: now, ExpiresAt: now.Add(time.Hour), LastSeenAt: now}); err != nil {
		t.Fatal(err)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := r.Cookie(sessionCookie); err != nil {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
		}
		h.ServeHTTP(w, r)
	})
}
