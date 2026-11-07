package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nutmos/expensetrackr/pkg/auth"
)

// tokenSetup registers alice (first run) and bob, each with a cookie client.
func tokenSetup(t *testing.T, srv *Server) (h http.Handler, alice, bob *authClient) {
	t.Helper()
	srv, _ = newAuthServer(t, srv)
	h = srv.Router()
	alice = &authClient{t: t, h: h, ip: "10.0.0.1"}
	if rec := alice.req("POST", "/api/auth/register", `{"username":"alice","password":"pw-12345"}`, nil); rec.Code != 201 {
		t.Fatalf("register alice: %d %s", rec.Code, rec.Body)
	}
	if rec := alice.req("POST", "/api/auth/register", `{"username":"bob","password":"pw-12345"}`, nil); rec.Code != 201 {
		t.Fatalf("register bob: %d %s", rec.Code, rec.Body)
	}
	bob = &authClient{t: t, h: h, ip: "10.0.0.2"}
	if rec := bob.req("POST", "/api/auth/login", `{"username":"bob","password":"pw-12345"}`, nil); rec.Code != 200 {
		t.Fatalf("login bob: %d %s", rec.Code, rec.Body)
	}
	return h, alice, bob
}

func createTok(t *testing.T, c *authClient, body string) createdToken {
	t.Helper()
	rec := c.req("POST", "/api/tokens", body, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create token %s: %d %s", body, rec.Code, rec.Body)
	}
	var out createdToken
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func bearer(tok string) map[string]string { return map[string]string{"Authorization": "Bearer " + tok} }

func TestAPITokenCreateListRevoke(t *testing.T) {
	_, alice, bob := tokenSetup(t, &Server{})
	tok := createTok(t, alice, `{"name":" Expense agent ","scopes":["write","read","write"],"expires_in_days":30}`)
	if !strings.HasPrefix(tok.Token, auth.APITokenPrefix) || tok.Prefix != tok.Token[:auth.APITokenDisplayLen] ||
		tok.Name != "Expense agent" || strings.Join(tok.Scopes, ",") != "read,write" || tok.ExpiresAt == nil {
		t.Fatalf("created: %+v", tok)
	}

	rec := alice.req("GET", "/api/tokens", "", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"count":1`) || strings.Contains(rec.Body.String(), tok.Token) ||
		strings.Contains(rec.Body.String(), "token_hash") || strings.Contains(rec.Body.String(), auth.HashToken(tok.Token)) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	if rec := bob.req("GET", "/api/tokens", "", nil); !strings.Contains(rec.Body.String(), `"count":0`) {
		t.Fatalf("bob list: %s", rec.Body)
	}

	// Bob cannot revoke Alice's token; bad uids are rejected.
	cases := []struct {
		name string
		c    *authClient
		uid  string
		want int
	}{
		{name: "other user", c: bob, uid: tok.UID, want: http.StatusNotFound},
		{name: "not a uuid", c: alice, uid: "nope", want: http.StatusBadRequest},
		{name: "owner", c: alice, uid: tok.UID, want: http.StatusNoContent},
		{name: "again", c: alice, uid: tok.UID, want: http.StatusNotFound},
	}
	for _, c := range cases {
		if rec := c.c.req("DELETE", "/api/tokens/"+c.uid, "", nil); rec.Code != c.want {
			t.Errorf("%s: revoke = %d, want %d (%s)", c.name, rec.Code, c.want, rec.Body)
		}
	}
	if rec := alice.req("GET", "/api/tokens", "", nil); !strings.Contains(rec.Body.String(), `"count":0`) {
		t.Errorf("after revoke: %s", rec.Body)
	}
}

func TestAPITokenValidation(t *testing.T) {
	_, alice, _ := tokenSetup(t, &Server{})
	cases := []struct{ name, body, field string }{
		{name: "no name", body: `{"scopes":["read"]}`, field: "name"},
		{name: "blank name", body: `{"name":"  ","scopes":["read"]}`, field: "name"},
		{name: "long name", body: `{"name":"` + strings.Repeat("x", MaxTokenNameLen+1) + `","scopes":["read"]}`, field: "name"},
		{name: "no scopes", body: `{"name":"Expense agent"}`, field: "scopes"},
		{name: "unknown scope", body: `{"name":"Expense agent","scopes":["admin"]}`, field: "scopes"},
		{name: "zero days", body: `{"name":"Expense agent","scopes":["read"],"expires_in_days":0}`, field: "expires_in_days"},
		{name: "too many days", body: `{"name":"Expense agent","scopes":["read"],"expires_in_days":99999}`, field: "expires_in_days"},
	}
	for _, c := range cases {
		rec := alice.req("POST", "/api/tokens", c.body, nil)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"`+c.field+`"`) {
			t.Errorf("%s: %d %s", c.name, rec.Code, rec.Body)
		}
	}
	if rec := alice.req("POST", "/api/tokens", `{"name":"x","scopes":["read"],"secret":"y"}`, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown field: %d", rec.Code)
	}
}

func TestAPITokenAuth(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	srv := &Server{Now: func() time.Time { return now }}
	h, alice, _ := tokenSetup(t, srv)
	write := createTok(t, alice, `{"name":"Expense agent","scopes":["write"]}`)
	read := createTok(t, alice, `{"name":"Read-only agent","scopes":["read"]}`)
	short := createTok(t, alice, `{"name":"Short-lived","scopes":["read"],"expires_in_days":1}`)
	revoked := createTok(t, alice, `{"name":"Old script","scopes":["write"]}`)
	if rec := alice.req("DELETE", "/api/tokens/"+revoked.UID, "", nil); rec.Code != 204 {
		t.Fatal("revoke setup")
	}
	anon := &authClient{t: t, h: h} // no cookie: Bearer only
	balance := `{"name":"Cash","type":"payment_account","currency":"SGD","balance":"0"}`
	cases := []struct {
		name, method, path, body, token string
		hdr                             map[string]string
		want                            int
	}{
		{name: "write GET", method: "GET", path: "/api/transactions", token: write.Token, want: 200},
		{name: "read GET", method: "GET", path: "/api/transactions", token: read.Token, want: 200},
		{name: "me", method: "GET", path: "/api/auth/me", token: read.Token, want: 200},
		{name: "write POST", method: "POST", path: "/api/balances", body: balance, token: write.Token, want: 201},
		{name: "write POST cross-site headers", method: "POST", path: "/api/balances", body: strings.Replace(balance, "Cash", "Card", 1), token: write.Token,
			hdr: map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"}, want: 201},
		{name: "read POST", method: "POST", path: "/api/balances", body: balance, token: read.Token, want: 403},
		{name: "bad token", method: "GET", path: "/api/transactions", token: "ext_nope", want: 401},
		{name: "no prefix", method: "GET", path: "/api/transactions", token: strings.TrimPrefix(write.Token, auth.APITokenPrefix), want: 401},
		{name: "revoked", method: "GET", path: "/api/transactions", token: revoked.Token, want: 401},
		{name: "list tokens", method: "GET", path: "/api/tokens", token: write.Token, want: 403},
		{name: "create token", method: "POST", path: "/api/tokens", body: `{"name":"x","scopes":["write"]}`, token: write.Token, want: 403},
		{name: "revoke token", method: "DELETE", path: "/api/tokens/" + read.UID, token: write.Token, want: 403},
		{name: "change password", method: "PUT", path: "/api/auth/password", body: `{"current_password":"pw-12345","new_password":"pw-67890"}`, token: write.Token, want: 403},
		{name: "logout", method: "POST", path: "/api/auth/logout", token: write.Token, want: 403},
		{name: "register", method: "POST", path: "/api/auth/register", body: `{"username":"carol","password":"pw-12345"}`, token: write.Token, want: 403},
	}
	for _, c := range cases {
		hdr := bearer(c.token)
		for k, v := range c.hdr {
			hdr[k] = v
		}
		if rec := anon.req(c.method, c.path, c.body, hdr); rec.Code != c.want {
			t.Errorf("%s: %s %s = %d, want %d (%s)", c.name, c.method, c.path, rec.Code, c.want, rec.Body)
		}
	}
	// A Bearer header wins over the cookie, so a session cannot lend a token
	// its rights.
	if rec := alice.req("GET", "/api/tokens", "", bearer(write.Token)); rec.Code != 403 {
		t.Errorf("cookie + bearer on /api/tokens: %d", rec.Code)
	}
	if rec := alice.req("GET", "/api/tokens", "", bearer("ext_bogus")); rec.Code != 401 {
		t.Errorf("cookie + bad bearer: %d", rec.Code)
	}

	// Expiry.
	now = now.Add(2 * 24 * time.Hour)
	if rec := anon.req("GET", "/api/transactions", "", bearer(short.Token)); rec.Code != 401 {
		t.Errorf("expired: %d", rec.Code)
	}
	if rec := anon.req("GET", "/api/transactions", "", bearer(write.Token)); rec.Code != 200 {
		t.Errorf("no-expiry token later: %d", rec.Code)
	}
}

func TestAPITokenDisabledUserAndTouch(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	srv := &Server{Now: func() time.Time { return now }}
	h, alice, _ := tokenSetup(t, srv)
	tok := createTok(t, alice, `{"name":"Expense agent","scopes":["read"]}`)
	anon := &authClient{t: t, h: h}
	db, err := sql.Open("pgx", lastAuthDB)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	lastUsed := func() string {
		var at sql.NullTime
		if err := db.QueryRow(`SELECT last_used_at FROM api_tokens WHERE uid = $1`, tok.UID).Scan(&at); err != nil {
			t.Fatal(err)
		}
		if !at.Valid {
			return ""
		}
		return at.Time.UTC().Format(time.RFC3339)
	}
	steps := []struct {
		advance time.Duration
		want    string
	}{
		{0, "2026-11-05T12:00:00Z"},
		{30 * time.Second, "2026-11-05T12:00:00Z"}, // throttled
		{time.Minute, "2026-11-05T12:01:30Z"},
	}
	for _, st := range steps {
		now = now.Add(st.advance)
		if rec := anon.req("GET", "/api/transactions", "", bearer(tok.Token)); rec.Code != 200 {
			t.Fatalf("request: %d", rec.Code)
		}
		if got := lastUsed(); got != st.want {
			t.Errorf("after +%s last_used_at = %q, want %q", st.advance, got, st.want)
		}
	}

	// Hash-only storage: the plaintext appears nowhere in the database.
	var hits int
	if err := db.QueryRow(`SELECT count(*) FROM api_tokens WHERE token_hash = $1 AND strpos(name || prefix || token_hash || scopes, $2) = 0`,
		auth.HashToken(tok.Token), tok.Token).Scan(&hits); err != nil || hits != 1 {
		t.Errorf("hash-only storage: hits=%d err=%v", hits, err)
	}

	if _, err := db.Exec(`UPDATE users SET status = 'disabled' WHERE username = 'alice'`); err != nil {
		t.Fatal(err)
	}
	if rec := anon.req("GET", "/api/transactions", "", bearer(tok.Token)); rec.Code != 401 {
		t.Errorf("disabled user: %d", rec.Code)
	}
}
