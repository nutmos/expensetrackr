package api

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/nutmos/expensetrackr/pkg/store"
	"github.com/nutmos/expensetrackr/pkg/user"

	"github.com/gin-gonic/gin"
)

func newTestServerWithStore(t *testing.T) (http.Handler, *store.Store) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
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

func TestUsersCRUD(t *testing.T) {
	h, st := newTestServerWithStore(t)

	rec, u := do(t, h, "POST", "/api/users", `{"display_name":" Nat ","username":"Nat","email":"Nat@Example.com","preferences":{"currency":"THB","theme":"dark"}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	uid, _ := u["uid"].(string)
	if !uuidV4RE.MatchString(uid) || rec.Header().Get("Location") != "/api/users/"+uid ||
		u["display_name"] != "Nat" || u["username"] != "nat" || u["email"] != "nat@example.com" ||
		u["status"] != "active" || u["has_password"] != false || u["email_verified_at"] != nil ||
		u["last_login_at"] != nil || u["password_updated_at"] != nil || u["updated_at"] != nil {
		t.Errorf("create body: %v", u)
	}
	if p, ok := u["preferences"].(map[string]any); !ok || p["currency"] != "THB" || p["theme"] != "dark" {
		t.Errorf("preferences: %#v", u["preferences"])
	}
	noSecrets(t, "create", rec.Body.String())

	// Minimal profile: only display_name; preferences default {}.
	rec, m := do(t, h, "POST", "/api/users", `{"display_name":"Guest"}`)
	if rec.Code != 201 || m["username"] != nil || m["email"] != nil || len(m["preferences"].(map[string]any)) != 0 {
		t.Errorf("minimal: %d %v", rec.Code, m)
	}
	guest := m["uid"].(string)

	// Validation / request errors.
	for _, c := range []struct {
		body   string
		status int
		field  string
	}{
		{`{}`, 422, "display_name"},
		{`{"display_name":"x","preferences":[1,2]}`, 422, "preferences"},
		{`{"display_name":"x","preferences":"dark"}`, 422, "preferences"},
		{`{"display_name":"x","preferences":42}`, 422, "preferences"},
		{`{"display_name":"x","email":"not-an-email"}`, 422, "email"},
		{`{"display_name":"x","username":"a b"}`, 422, "username"},
		{`{"display_name":"x","username":"NAT"}`, 409, "username"},
		{`{"display_name":"x","email":"NAT@example.COM"}`, 409, "email"},
		{`{"display_name":"x","password":"hunter2"}`, 400, ""},
		{`{"display_name":"x","password_hash":"$argon2id$x"}`, 400, ""},
		{`{"display_name":"x","preferences":{"a":}}`, 400, ""}, // malformed JSON body
		{`[]`, 400, ""},
	} {
		rec, body := do(t, h, "POST", "/api/users", c.body)
		if rec.Code != c.status || (c.field != "" && fieldsOf(body)[c.field] == nil) {
			t.Errorf("POST %s: %d %v, want %d on %q", c.body, rec.Code, body, c.status, c.field)
		}
	}

	path := "/api/users/" + uid
	// A password set by the (future) auth layer is never exposed.
	if err := st.SetPasswordHash(context.Background(), uid, strPtr("$argon2id$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA")); err != nil {
		t.Fatal(err)
	}
	rec, got := do(t, h, "GET", path, "")
	if rec.Code != 200 || got["has_password"] != true || got["password_updated_at"] == nil {
		t.Errorf("get after password: %d %v", rec.Code, got)
	}
	noSecrets(t, "get", rec.Body.String())
	rec, list := do(t, h, "GET", "/api/users", "")
	if rec.Code != 200 || list["count"].(float64) != 3 { // + the logged-in "tester"
		t.Errorf("list: %d %v", rec.Code, list)
	}
	noSecrets(t, "list", rec.Body.String())

	// PATCH: preferences replaced as a whole; read-only fields ignored.
	rec, got = do(t, h, "PATCH", path, `{"preferences":{"theme":"light"},"status":"disabled","has_password":false,"uid":"x"}`)
	if rec.Code != 200 || got["status"] != "active" || got["has_password"] != true || got["uid"] != uid || got["updated_at"] == nil {
		t.Errorf("patch: %d %v", rec.Code, got)
	}
	if p := got["preferences"].(map[string]any); len(p) != 1 || p["theme"] != "light" {
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
		{`{"username":"guest-taken"}`, 200},
	} {
		if rec, body := do(t, h, "PATCH", path, c.body); rec.Code != c.status {
			t.Errorf("PATCH %s: %d %v, want %d", c.body, rec.Code, body, c.status)
		}
	}
	if rec, body := do(t, h, "PATCH", "/api/users/"+guest, `{"username":"Guest-Taken"}`); rec.Code != 409 || fieldsOf(body)["username"] == nil {
		t.Errorf("PATCH dup username: %d %v", rec.Code, body)
	}
	if rec, body := do(t, h, "PATCH", path, `{"email":null}`); rec.Code != 200 || body["email"] != nil {
		t.Errorf("PATCH clear email: %d %v", rec.Code, body)
	}

	// PUT: full replace of profile fields; a GET response round-trips.
	_, cur := do(t, h, "GET", path, "")
	rec, got = do(t, h, "PUT", path, mustJSON(t, cur))
	if rec.Code != 200 || got["username"] != "guest-taken" || got["has_password"] != true {
		t.Errorf("PUT round-trip: %d %v", rec.Code, got)
	}
	rec, got = do(t, h, "PUT", path, `{"display_name":"Nat E."}`)
	if rec.Code != 200 || got["username"] != nil || got["email"] != nil || len(got["preferences"].(map[string]any)) != 0 || got["has_password"] != true {
		t.Errorf("PUT minimal clears profile fields only: %d %v", rec.Code, got)
	}
	noSecrets(t, "put", rec.Body.String())

	// Identities: read-only list (empty until SSO exists; store-created here).
	rec, ids := do(t, h, "GET", path+"/identities", "")
	if rec.Code != 200 || ids["count"].(float64) != 0 {
		t.Errorf("identities empty: %d %v", rec.Code, ids)
	}
	if err := st.CreateIdentity(context.Background(), &user.Identity{UserUID: uid, Provider: user.ProviderApple, ProviderSubject: "001234.abc"}); err != nil {
		t.Fatal(err)
	}
	rec, ids = do(t, h, "GET", path+"/identities", "")
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
	const unknown = "/api/users/00000000-0000-4000-8000-000000000000"
	for _, c := range [][2]string{{"GET", ""}, {"PUT", `{"display_name":"x"}`}, {"PATCH", `{"display_name":"x"}`}, {"DELETE", ""}} {
		if rec, _ := do(t, h, c[0], unknown, c[1]); rec.Code != 404 {
			t.Errorf("%s unknown: %d", c[0], rec.Code)
		}
	}
	if rec, _ := do(t, h, "GET", unknown+"/identities", ""); rec.Code != 404 {
		t.Errorf("identities unknown user: %d", rec.Code)
	}

	// DELETE removes the user and its identities.
	if rec, _ := do(t, h, "DELETE", path, ""); rec.Code != 204 {
		t.Errorf("delete: %d", rec.Code)
	}
	if rec, _ := do(t, h, "GET", path, ""); rec.Code != 404 {
		t.Errorf("get after delete: %d", rec.Code)
	}
	if ids, _ := st.ListIdentities(context.Background(), uid); len(ids) != 0 {
		t.Errorf("identities after delete: %v", ids)
	}
}

func strPtr(s string) *string { return &s }

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
