package api

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/nutmos/expensetrackr/pkg/auth"
	"github.com/nutmos/expensetrackr/pkg/store"
	"github.com/nutmos/expensetrackr/pkg/user"

	"github.com/gin-gonic/gin"
)

// asUser returns a handler that sends every request with a session of a newly
// registered user name.
func asUser(t *testing.T, router http.Handler, st *store.Store, name string) http.Handler {
	t.Helper()
	ctx := context.Background()
	u := user.User{Username: &name, DisplayName: name, Preferences: []byte("{}")}
	if err := st.RegisterUser(ctx, &u, "$2a$04$unused", false); err != nil {
		t.Fatal(err)
	}
	tok, _ := auth.NewToken()
	now := time.Now()
	if err := st.CreateSession(ctx, &store.Session{TokenHash: auth.HashToken(tok), UserUID: u.UID, CreatedAt: now, ExpiresAt: now.Add(time.Hour), LastSeenAt: now}); err != nil {
		t.Fatal(err)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
		router.ServeHTTP(w, r)
	})
}

// TestCategoriesArePerUser: each logged-in user sees and edits only their own
// categories; another user's uid is 404 and cannot be used on a transaction.
func TestCategoriesArePerUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	router := (&Server{Store: st, Static: fstest.MapFS{"index.html": {Data: []byte("x")}}}).Router()
	alice := asUser(t, router, st, "alice")
	bob := asUser(t, router, st, "bob")

	food := mkCat(t, alice, "Food", "expense")["uid"].(string)
	mkCat(t, bob, "Food", "expense") // same name for another user: fine
	path := "/api/categories/" + food

	if _, l := do(t, bob, "GET", "/api/categories", ""); l["count"].(float64) != 1 {
		t.Errorf("bob list: %v", l)
	}
	for _, m := range []struct{ method, body string }{{"GET", ""}, {"PUT", `{"name":"x","type":"expense"}`}, {"DELETE", ""}} {
		if rec, _ := do(t, bob, m.method, path, m.body); rec.Code != 404 {
			t.Errorf("bob %s alice's category: %d", m.method, rec.Code)
		}
	}
	if rec, _ := do(t, alice, "GET", path, ""); rec.Code != 200 {
		t.Errorf("alice get: %d", rec.Code)
	}

	// Transactions validate category_uid against the current user's categories.
	cash := seedPayable(t, alice, "Cash", "THB")
	body := `{"amount":"10","currency":"THB","balance_uid":"` + cash + `","spent_at":"2026-10-01T10:00:00+07:00","category_uid":"` + food + `"}`
	if rec, b := do(t, bob, "POST", "/api/transactions", body); rec.Code != 422 {
		t.Errorf("bob uses alice's category: %d %v", rec.Code, b)
	}
	rec, b := do(t, alice, "POST", "/api/transactions", body)
	if rec.Code != 201 {
		t.Fatalf("alice tx: %d %v", rec.Code, b)
	}
	// In use: delete and type change are 409.
	if rec, _ := do(t, alice, "DELETE", path, ""); rec.Code != 409 {
		t.Errorf("delete in use: %d", rec.Code)
	}
	if rec, _ := do(t, alice, "PUT", path, `{"name":"Food","type":"income"}`); rec.Code != 409 {
		t.Errorf("type change in use: %d", rec.Code)
	}
}

func TestFaviconNoContent(t *testing.T) {
	h := newTestServer(t)
	if rec, _ := do(t, h, "GET", "/favicon.ico", ""); rec.Code != http.StatusNoContent {
		t.Errorf("favicon: %d", rec.Code)
	}
}
