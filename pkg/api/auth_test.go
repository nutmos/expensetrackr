package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nutmos/expensetrackr/pkg/auth"
	"github.com/nutmos/expensetrackr/pkg/store"

	"github.com/gin-gonic/gin"
)

// authClient talks to a fresh, unauthenticated server and keeps its cookie.
type authClient struct {
	t      *testing.T
	h      http.Handler
	cookie string
	ip     string
}

var lastAuthDB string

func newAuthServer(t *testing.T, srv *Server) (*Server, *store.Store) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	lastAuthDB = filepath.Join(t.TempDir(), "auth.db")
	st, err := store.Open(lastAuthDB)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv.Store = st
	return srv, st
}

func (c *authClient) req(method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	c.t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if c.ip != "" {
		r.RemoteAddr = c.ip + ":1234"
	}
	if c.cookie != "" {
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: c.cookie})
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, r)
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == sessionCookie {
			c.cookie = ck.Value
			if ck.MaxAge < 0 {
				c.cookie = ""
			}
		}
	}
	return rec
}

func TestAuthFirstRunRegisterLoginLogout(t *testing.T) {
	srv, _ := newAuthServer(t, &Server{})
	h := srv.Router()
	c := &authClient{t: t, h: h}

	// Protected routes need a session; first run is signalled.
	rec := c.req("GET", "/api/transactions", "", nil)
	if rec.Code != 401 || !strings.Contains(rec.Body.String(), "setup_required") {
		t.Fatalf("unauth list: %d %s", rec.Code, rec.Body)
	}
	if rec := c.req("GET", "/api/healthz", "", nil); rec.Code != 200 {
		t.Fatalf("healthz: %d", rec.Code)
	}
	if rec := c.req("GET", "/api/openapi.json", "", nil); rec.Code != 200 {
		t.Fatalf("spec: %d", rec.Code)
	}

	// Validation.
	if rec := c.req("POST", "/api/auth/register", `{"username":"A!","password":"short"}`, nil); rec.Code != 422 ||
		!strings.Contains(rec.Body.String(), `"username"`) || !strings.Contains(rec.Body.String(), `"password"`) {
		t.Fatalf("register invalid: %d %s", rec.Code, rec.Body)
	}
	if rec := c.req("POST", "/api/auth/register", `{"username":"nat","password":"pw-12345","email":"x@y.z"}`, nil); rec.Code != 400 {
		t.Fatalf("register with email field: %d", rec.Code)
	}

	// First-run registration logs in.
	rec = c.req("POST", "/api/auth/register", `{"username":"Nat","password":"pw-12345","display_name":"Nat E"}`, nil)
	if rec.Code != 201 || c.cookie == "" {
		t.Fatalf("register: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "password_hash") || strings.Contains(rec.Body.String(), "$2a$") ||
		!strings.Contains(rec.Body.String(), `"username":"nat"`) || !strings.Contains(rec.Body.String(), `"has_password":true`) {
		t.Fatalf("register body: %s", rec.Body)
	}
	setCookie := rec.Header().Get("Set-Cookie")
	for _, want := range []string{"HttpOnly", "SameSite=Lax", "Path=/"} {
		if !strings.Contains(setCookie, want) {
			t.Errorf("cookie %q lacks %s", setCookie, want)
		}
	}
	if strings.Contains(setCookie, "Secure") {
		t.Error("Secure cookie on plain HTTP")
	}
	if rec := c.req("GET", "/api/auth/me", "", nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"display_name":"Nat E"`) {
		t.Fatalf("me: %d %s", rec.Code, rec.Body)
	}
	if rec := c.req("GET", "/api/transactions", "", nil); rec.Code != 200 {
		t.Fatalf("list logged in: %d", rec.Code)
	}

	// Logged in: may register another user without switching sessions.
	me := c.cookie
	if rec := c.req("POST", "/api/auth/register", `{"username":"guest","password":"guest-pass"}`, nil); rec.Code != 201 || c.cookie != me {
		t.Fatalf("register 2nd while logged in: %d %s", rec.Code, rec.Body)
	}
	if rec := c.req("POST", "/api/auth/register", `{"username":"GUEST","password":"guest-pass"}`, nil); rec.Code != 409 {
		t.Fatalf("duplicate username: %d", rec.Code)
	}

	// Logout.
	if rec := c.req("POST", "/api/auth/logout", "", nil); rec.Code != 204 || c.cookie != "" {
		t.Fatalf("logout: %d cookie=%q", rec.Code, c.cookie)
	}
	old := &authClient{t: t, h: h, cookie: me}
	if rec := old.req("GET", "/api/auth/me", "", nil); rec.Code != 401 || !strings.Contains(rec.Body.String(), "unauthenticated") {
		t.Fatalf("old session after logout: %d %s", rec.Code, rec.Body)
	}

	// Registration is closed when logged out.
	if rec := c.req("POST", "/api/auth/register", `{"username":"eve","password":"eve-pass1"}`, nil); rec.Code != 403 {
		t.Fatalf("register after setup: %d", rec.Code)
	}

	// Login: wrong password, unknown user (same message), then success.
	for _, body := range []string{`{"username":"nat","password":"wrong-pass"}`, `{"username":"nobody","password":"pw-12345"}`} {
		rec := c.req("POST", "/api/auth/login", body, nil)
		if rec.Code != 401 || !strings.Contains(rec.Body.String(), "invalid username or password") || c.cookie != "" {
			t.Fatalf("bad login %s: %d %s", body, rec.Code, rec.Body)
		}
	}
	rec = c.req("POST", "/api/auth/login", `{"username":"NAT","password":"pw-12345"}`, nil)
	if rec.Code != 200 || c.cookie == "" || !strings.Contains(rec.Body.String(), `"last_login_at":"`) {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
}

func TestAuthDisabledUserCannotLogin(t *testing.T) {
	srv, _ := newAuthServer(t, &Server{})
	c := &authClient{t: t, h: srv.Router()}
	c.req("POST", "/api/auth/register", `{"username":"nat","password":"pw-12345"}`, nil)
	c.req("POST", "/api/auth/logout", "", nil)
	db, err := sql.Open("sqlite", "file:"+lastAuthDB)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE users SET status = 'disabled'`); err != nil {
		t.Fatal(err)
	}
	if rec := c.req("POST", "/api/auth/login", `{"username":"nat","password":"pw-12345"}`, nil); rec.Code != 401 {
		t.Fatalf("disabled login: %d", rec.Code)
	}
}

func TestLoginThrottle(t *testing.T) {
	now := time.Now()
	th := auth.NewThrottle()
	th.Now = func() time.Time { return now }
	srv, _ := newAuthServer(t, &Server{Throttle: th})
	h := srv.Router()
	c := &authClient{t: t, h: h, ip: "10.0.0.1"}
	c.req("POST", "/api/auth/register", `{"username":"nat","password":"pw-12345"}`, nil)
	c.req("POST", "/api/auth/logout", "", nil)
	for i := 0; i < 5; i++ {
		if rec := c.req("POST", "/api/auth/login", `{"username":"nat","password":"nope-nope"}`, nil); rec.Code != 401 {
			t.Fatalf("failure %d: %d", i, rec.Code)
		}
	}
	rec := c.req("POST", "/api/auth/login", `{"username":"nat","password":"pw-12345"}`, nil)
	if rec.Code != 429 || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("locked login: %d %v", rec.Code, rec.Header())
	}
	other := &authClient{t: t, h: h, ip: "10.0.0.2"}
	if rec := other.req("POST", "/api/auth/login", `{"username":"nat","password":"pw-12345"}`, nil); rec.Code != 200 {
		t.Fatalf("other IP: %d", rec.Code)
	}
	now = now.Add(6 * time.Minute)
	if rec := c.req("POST", "/api/auth/login", `{"username":"nat","password":"pw-12345"}`, nil); rec.Code != 200 {
		t.Fatalf("after lockout: %d", rec.Code)
	}
}

func TestSessionExpiry(t *testing.T) {
	now := time.Now()
	srv, _ := newAuthServer(t, &Server{Now: func() time.Time { return now }})
	c := &authClient{t: t, h: srv.Router()}
	c.req("POST", "/api/auth/register", `{"username":"nat","password":"pw-12345"}`, nil)
	// Sliding: use after 29 days keeps it alive another 30.
	now = now.Add(29 * 24 * time.Hour)
	if rec := c.req("GET", "/api/auth/me", "", nil); rec.Code != 200 {
		t.Fatalf("day 29: %d", rec.Code)
	}
	now = now.Add(29 * 24 * time.Hour)
	if rec := c.req("GET", "/api/auth/me", "", nil); rec.Code != 200 {
		t.Fatalf("day 58 after use: %d", rec.Code)
	}
	now = now.Add(31 * 24 * time.Hour)
	if rec := c.req("GET", "/api/auth/me", "", nil); rec.Code != 401 {
		t.Fatalf("idle 31 days: %d", rec.Code)
	}
}

func TestPasswordChangeInvalidatesOtherSessions(t *testing.T) {
	srv, _ := newAuthServer(t, &Server{})
	h := srv.Router()
	a := &authClient{t: t, h: h}
	a.req("POST", "/api/auth/register", `{"username":"nat","password":"pw-12345"}`, nil)
	b := &authClient{t: t, h: h}
	if rec := b.req("POST", "/api/auth/login", `{"username":"nat","password":"pw-12345"}`, nil); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	if rec := a.req("PUT", "/api/auth/password", `{"current_password":"bad-bad-bad","new_password":"new-pass-1"}`, nil); rec.Code != 403 {
		t.Fatalf("wrong current: %d", rec.Code)
	}
	if rec := a.req("PUT", "/api/auth/password", `{"current_password":"pw-12345","new_password":"short"}`, nil); rec.Code != 422 {
		t.Fatalf("short new: %d", rec.Code)
	}
	if rec := a.req("PUT", "/api/auth/password", `{"current_password":"pw-12345","new_password":"new-pass-1"}`, nil); rec.Code != 204 {
		t.Fatalf("change: %d %s", rec.Code, rec.Body)
	}
	if rec := a.req("GET", "/api/auth/me", "", nil); rec.Code != 200 {
		t.Fatalf("own session after change: %d", rec.Code)
	}
	if rec := b.req("GET", "/api/auth/me", "", nil); rec.Code != 401 {
		t.Fatalf("other session after change: %d", rec.Code)
	}
	c := &authClient{t: t, h: h}
	if rec := c.req("POST", "/api/auth/login", `{"username":"nat","password":"pw-12345"}`, nil); rec.Code != 401 {
		t.Fatalf("old password: %d", rec.Code)
	}
	if rec := c.req("POST", "/api/auth/login", `{"username":"nat","password":"new-pass-1"}`, nil); rec.Code != 200 {
		t.Fatalf("new password: %d", rec.Code)
	}
}

func TestProtectedRoutes401(t *testing.T) {
	srv, _ := newAuthServer(t, &Server{})
	h := srv.Router()
	c := &authClient{t: t, h: h}
	c.req("POST", "/api/auth/register", `{"username":"nat","password":"pw-12345"}`, nil)
	anon := &authClient{t: t, h: h, cookie: "not-a-real-token"}
	for _, rt := range h.Routes() {
		if !strings.HasPrefix(rt.Path, "/api/") || publicAPI[rt.Method+" "+rt.Path] {
			continue
		}
		path := strings.ReplaceAll(rt.Path, ":uid", "1a8af4f6-006a-4712-baba-eb8f433bccb1")
		body := ""
		if rt.Method != "GET" && rt.Method != "DELETE" {
			body = "{}"
		}
		rec := anon.req(rt.Method, path, body, nil)
		if rec.Code != 401 || !strings.Contains(rec.Body.String(), `"error"`) {
			t.Errorf("%s %s without session: %d %s", rt.Method, path, rec.Code, rec.Body)
		}
	}
}

func TestCSRFGuard(t *testing.T) {
	srv, _ := newAuthServer(t, &Server{})
	h := srv.Router()
	c := &authClient{t: t, h: h}
	c.req("POST", "/api/auth/register", `{"username":"nat","password":"pw-12345"}`, nil)
	cat := `{"name":"Food","type":"expense"}`
	cases := []struct {
		hdr  map[string]string
		want int
	}{
		{map[string]string{"Origin": "https://evil.example"}, 403},
		{map[string]string{"Origin": "null"}, 403},
		{map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		{map[string]string{"Sec-Fetch-Site": "same-site"}, 403},
		{map[string]string{"Content-Type": "text/plain"}, 415},
		{map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, 415},
		{map[string]string{"Origin": "http://example.com", "Sec-Fetch-Site": "same-origin"}, 201},
	}
	for _, tc := range cases {
		if rec := c.req("POST", "/api/categories", cat, tc.hdr); rec.Code != tc.want {
			t.Errorf("%v: %d want %d (%s)", tc.hdr, rec.Code, tc.want, rec.Body)
		}
	}
	// Login is guarded too (login CSRF).
	if rec := c.req("POST", "/api/auth/login", `{"username":"nat","password":"pw-12345"}`, map[string]string{"Origin": "https://evil.example"}); rec.Code != 403 {
		t.Errorf("cross-origin login: %d", rec.Code)
	}
	// Reads are not affected.
	if rec := c.req("GET", "/api/categories", "", map[string]string{"Sec-Fetch-Site": "cross-site"}); rec.Code != 200 {
		t.Errorf("cross-site GET: %d", rec.Code)
	}
}
