package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/nutmos/expensetrackr/pkg/store"
	"github.com/nutmos/expensetrackr/pkg/store/storetest"
	"github.com/nutmos/expensetrackr/pkg/user"

	"github.com/gin-gonic/gin"
)

func newTestServerWithStore(t *testing.T) (http.Handler, *store.Store) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	st := storetest.Open(t)
	static := fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>Expense Log</title>")}}
	return loggedIn(t, (&Server{Store: st, Static: static}).Router(), st), st
}

// noSecrets fails if a response body mentions the hash or its column.
func noSecrets(t *testing.T, label, body string) {
	t.Helper()
	for _, bad := range []string{"password_hash", "argon2id", `"id":`} {
		if strings.Contains(body, bad) {
			t.Errorf("%s: response contains %q: %s", label, bad, body)
		}
	}
}

// otherUser creates, directly in the store, a second account with a
// password ("other", other@example.com) that the logged-in tester must not
// be able to see or change through the API.
func otherUser(t *testing.T, st *store.Store) user.User {
	t.Helper()
	name, email := "other", "other@example.com"
	u := user.User{Username: &name, DisplayName: "Other"}
	if err := st.RegisterUser(context.Background(), &u, "$2a$04$unused", false); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateUser(context.Background(), u.ID, func(cur user.User) (user.User, error) {
		cur.Email = &email
		return cur, nil
	}); err != nil {
		t.Fatal(err)
	}
	return u
}

// TestOwnAccountAPI: the logged-in user reads and edits their own profile
// through /api/users/:uid (issue #76 limits these routes to it).
func TestOwnAccountAPI(t *testing.T) {
	h, st := newTestServerWithStore(t) // logged in as "tester"
	_, me := do(t, h, "GET", "/api/auth/me", "")
	uid := me["uid"].(string)
	path := "/api/users/" + uid
	other := otherUser(t, st)

	rec, got := do(t, h, "GET", path, "")
	if rec.Code != 200 || got["uid"] != uid || got["username"] != "tester" || got["status"] != "active" ||
		got["has_password"] != true || got["password_updated_at"] == nil || got["preferences"].(map[string]any)["base_currency"] != user.DefaultBaseCurrency {
		t.Errorf("get own: %d %v", rec.Code, got)
	}
	noSecrets(t, "get", rec.Body.String())

	// The list holds only the caller.
	rec, list := do(t, h, "GET", "/api/users", "")
	if items, _ := list["users"].([]any); rec.Code != 200 || list["count"].(float64) != 1 || len(items) != 1 || items[0].(map[string]any)["uid"] != uid {
		t.Errorf("list: %d %v", rec.Code, list)
	}
	noSecrets(t, "list", rec.Body.String())

	// Validation / request errors (PUT validates like the old create did).
	for _, c := range []struct {
		body   string
		status int
		field  string
	}{
		{`{}`, 422, "display_name"},
		{`{"display_name":"x","username":"tester","preferences":[1,2]}`, 422, "preferences"},
		{`{"display_name":"x","username":"tester","preferences":"dark"}`, 422, "preferences"},
		{`{"display_name":"x","username":"tester","preferences":42}`, 422, "preferences"},
		{`{"display_name":"x","username":"tester","preferences":{"theme":"dark"}}`, 422, "preferences"},
		{`{"display_name":"x","username":"tester","preferences":{"base_currency":"XYZ"}}`, 422, "preferences.base_currency"},
		{`{"display_name":"x","username":"tester","email":"not-an-email"}`, 422, "email"},
		{`{"display_name":"x","username":"a b"}`, 422, "username"},
		{`{"display_name":"x","username":"OTHER"}`, 409, "username"},
		{`{"display_name":"x","username":"tester","email":"OTHER@example.COM"}`, 409, "email"},
		{`{"display_name":"x","password":"hunter2"}`, 400, ""},
		{`{"display_name":"x","password_hash":"$argon2id$x"}`, 400, ""},
		{`{"display_name":"x","preferences":{"a":}}`, 400, ""}, // malformed JSON body
		{`[]`, 400, ""},
	} {
		rec, body := do(t, h, "PUT", path, c.body)
		if rec.Code != c.status || (c.field != "" && fieldsOf(body)[c.field] == nil) {
			t.Errorf("PUT %s: %d %v, want %d on %q", c.body, rec.Code, body, c.status, c.field)
		}
	}

	// PATCH: preferences replaced as a whole; read-only fields ignored.
	rec, got = do(t, h, "PATCH", path, `{"preferences":{"base_currency":"EUR"},"status":"disabled","has_password":false,"uid":"x"}`)
	if rec.Code != 200 || got["status"] != "active" || got["has_password"] != true || got["uid"] != uid || got["updated_at"] == nil {
		t.Errorf("patch: %d %v", rec.Code, got)
	}
	if p := got["preferences"].(map[string]any); len(p) != 1 || p["base_currency"] != "EUR" {
		t.Errorf("patch replaces preferences: %v", p)
	}
	noSecrets(t, "patch", rec.Body.String())
	for _, c := range []struct {
		body   string
		status int
	}{
		{`{}`, 400},
		{`{"password":"x"}`, 400},
		{`{"display_name":null}`, 422},
		{`{"preferences":[1]}`, 422},
		{`{"username":"Other"}`, 409},
		{`{"username":"Tester-2","email":"tester@example.com"}`, 200},
	} {
		if rec, body := do(t, h, "PATCH", path, c.body); rec.Code != c.status {
			t.Errorf("PATCH %s: %d %v, want %d", c.body, rec.Code, body, c.status)
		}
	}
	if rec, body := do(t, h, "PATCH", path, `{"email":null}`); rec.Code != 200 || body["email"] != nil || body["username"] != "tester-2" {
		t.Errorf("PATCH clear email: %d %v", rec.Code, body)
	}

	// PUT: full replace of profile fields; a GET response round-trips.
	_, cur := do(t, h, "GET", path, "")
	rec, got = do(t, h, "PUT", path, mustJSON(t, cur))
	if rec.Code != 200 || got["username"] != "tester-2" || got["has_password"] != true {
		t.Errorf("PUT round-trip: %d %v", rec.Code, got)
	}
	// A user with a password keeps their username, the login name (#77).
	rec, got = do(t, h, "PUT", path, `{"display_name":"Tester T."}`)
	if rec.Code != 422 || got["code"] != "username_required" || fieldsOf(got)["username"] == nil {
		t.Errorf("PUT without username: %d %v", rec.Code, got)
	}
	rec, got = do(t, h, "PUT", path, `{"display_name":"Tester T.","username":"tester"}`)
	if rec.Code != 200 || got["username"] != "tester" || got["email"] != nil || got["preferences"].(map[string]any)["base_currency"] != user.DefaultBaseCurrency {
		t.Errorf("PUT minimal clears profile fields only: %d %v", rec.Code, got)
	}
	noSecrets(t, "put", rec.Body.String())

	// Identities: read-only list (empty until SSO exists; store-created here).
	if rec, ids := do(t, h, "GET", path+"/identities", ""); rec.Code != 200 || ids["count"].(float64) != 0 {
		t.Errorf("identities empty: %d %v", rec.Code, ids)
	}
	if err := st.CreateIdentity(context.Background(), &user.Identity{UserUID: uid, Provider: user.ProviderApple, ProviderSubject: "001234.abc"}); err != nil {
		t.Fatal(err)
	}
	rec, ids := do(t, h, "GET", path+"/identities", "")
	items, _ := ids["identities"].([]any)
	if rec.Code != 200 || len(items) != 1 || items[0].(map[string]any)["provider"] != "apple" || items[0].(map[string]any)["user_uid"] != uid {
		t.Errorf("identities: %d %v", rec.Code, ids)
	}
	noSecrets(t, "identities", rec.Body.String())
	if rec, _ := do(t, h, "POST", path+"/identities", `{}`); rec.Code != http.StatusNotFound {
		t.Errorf("identities are read-only: %d", rec.Code)
	}

	// uid-only paths.
	for _, p := range []string{"/api/users/1", "/api/users/abc", "/api/users/abc/identities"} {
		if rec, _ := do(t, h, "GET", p, ""); rec.Code != 400 {
			t.Errorf("GET %s: %d, want 400", p, rec.Code)
		}
	}

	// The other account is still untouched by everything above.
	if u, err := st.GetUserByUID(context.Background(), other.UID); err != nil || *u.Username != "other" || *u.Email != "other@example.com" {
		t.Errorf("other user changed: %+v %v", u, err)
	}
}

// TestUserRoutesOnlyReachOwnAccount (issue #76): another user's account is
// a 404 on every /api/users route, the same as an unknown uid, and is left
// unchanged; POST /api/users is disabled.
func TestUserRoutesOnlyReachOwnAccount(t *testing.T) {
	h, st := newTestServerWithStore(t) // logged in as "tester"
	other := otherUser(t, st)
	if err := st.CreateIdentity(context.Background(), &user.Identity{UserUID: other.UID, Provider: user.ProviderGoogle, ProviderSubject: "sub-1"}); err != nil {
		t.Fatal(err)
	}
	const unknown = "00000000-0000-4000-8000-000000000000"
	for _, target := range []string{other.UID, strings.ToUpper(other.UID), unknown} {
		base := "/api/users/" + target
		for _, c := range []struct{ method, path, body string }{
			{"GET", base, ""},
			{"PUT", base, `{"display_name":"Hijacked","username":"hijacked"}`},
			{"PATCH", base, `{"display_name":"Hijacked"}`},
			{"DELETE", base, ""},
			{"GET", base + "/identities", ""},
			{"GET", base + "/preferences", ""},
			{"PUT", base + "/preferences", `{"base_currency":"EUR"}`},
		} {
			rec, body := do(t, h, c.method, c.path, c.body)
			if rec.Code != http.StatusNotFound || body["error"] != "user not found" {
				t.Errorf("%s %s: %d %v, want 404 user not found", c.method, c.path, rec.Code, body)
			}
		}
	}
	got, err := st.GetUserByUID(context.Background(), other.UID)
	if err != nil || got.DisplayName != "Other" || *got.Username != "other" || got.Preferences.BaseCurrency != "" {
		t.Errorf("other user changed: %+v %v", got, err)
	}
	if ids, _ := st.ListIdentities(context.Background(), other.UID); len(ids) != 1 {
		t.Errorf("other user's identities: %v", ids)
	}

	// Creating profiles through this API is disabled.
	for _, body := range []string{`{"display_name":"Guest"}`, `{}`, `[]`} {
		rec, b := do(t, h, "POST", "/api/users", body)
		if rec.Code != http.StatusForbidden || b["code"] != "use_register" {
			t.Errorf("POST /api/users %s: %d %v, want 403 use_register", body, rec.Code, b)
		}
	}
	if us, _ := st.ListUsers(context.Background()); len(us) != 2 {
		t.Errorf("users after refused creates: %d", len(us))
	}
}

// TestDeleteOwnAccount: a user can delete their own account once another
// login account exists (#77); the session ends with it.
func TestDeleteOwnAccount(t *testing.T) {
	h, st := newTestServerWithStore(t)
	_, me := do(t, h, "GET", "/api/auth/me", "")
	path := "/api/users/" + me["uid"].(string)
	otherUser(t, st)
	if rec, b := do(t, h, "DELETE", path, ""); rec.Code != 204 {
		t.Fatalf("delete own account: %d %v", rec.Code, b)
	}
	if rec, _ := do(t, h, "GET", "/api/auth/me", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("session after deleting the account: %d, want 401", rec.Code)
	}
	if _, err := st.GetUserByUID(context.Background(), me["uid"].(string)); err == nil {
		t.Error("account still exists")
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestAccountLockoutGuards (issue #77): the profile API can neither strand a
// password account without its login name nor delete the last account that
// can log in, which would reopen first-run registration to anyone.
func TestAccountLockoutGuards(t *testing.T) {
	h, st := newTestServerWithStore(t) // logged in as "tester", the only password user
	_, me := do(t, h, "GET", "/api/auth/me", "")
	mePath := "/api/users/" + me["uid"].(string)

	for _, c := range []struct{ method, body string }{
		{"PATCH", `{"username":null}`},
		{"PATCH", `{"username":""}`},
		{"PUT", `{"display_name":"Tester"}`},
	} {
		rec, b := do(t, h, c.method, mePath, c.body)
		if rec.Code != 422 || b["code"] != "username_required" {
			t.Errorf("%s %s: %d %v, want 422 username_required", c.method, c.body, rec.Code, b)
		}
	}
	if _, got := do(t, h, "GET", mePath, ""); got["username"] != "tester" {
		t.Errorf("username after refused updates: %v", got["username"])
	}

	// Users without a password may still drop their username (store rule;
	// such profiles can't be reached through the API since issue #76).
	guestName := "guest"
	guest := user.User{Username: &guestName, DisplayName: "Guest"}
	if err := st.CreateUser(context.Background(), &guest); err != nil {
		t.Fatal(err)
	}
	if got, err := st.UpdateUser(context.Background(), guest.ID, func(cur user.User) (user.User, error) {
		cur.Username = nil
		return cur, nil
	}); err != nil || got.Username != nil {
		t.Errorf("guest clears username: %+v %v", got, err)
	}

	// The last password user cannot be deleted; deleting a password-less
	// user is fine.
	if rec, b := do(t, h, "DELETE", mePath, ""); rec.Code != 409 || b["code"] != "last_login_user" {
		t.Errorf("delete last login user: %d %v, want 409 last_login_user", rec.Code, b)
	}
	if err := st.DeleteUser(context.Background(), guest.ID); err != nil {
		t.Errorf("delete guest: %v", err)
	}
	// Registration stays closed to anonymous visitors.
	anon := (&Server{Store: st, Static: fstest.MapFS{"index.html": {}}}).Router()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/auth/register", strings.NewReader(`{"username":"intruder","password":"pw-12345678"}`))
	req.Header.Set("Content-Type", "application/json")
	anon.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "registration_closed") {
		t.Errorf("anonymous register: %d %s", rec.Code, rec.Body)
	}

	// With a second password user, the first can be deleted.
	if rec, b := do(t, h, "POST", "/api/auth/register", `{"username":"admin2","password":"pw-12345678"}`); rec.Code != 201 {
		t.Fatalf("register second user: %d %v", rec.Code, b)
	}
	if rec, b := do(t, h, "DELETE", mePath, ""); rec.Code != 204 {
		t.Errorf("delete with another login user left: %d %v", rec.Code, b)
	}
	if has, err := st.HasPasswordUser(context.Background()); err != nil || !has {
		t.Errorf("a password user must remain: %v %v", has, err)
	}
}
