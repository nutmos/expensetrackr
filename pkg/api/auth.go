package api

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nutmos/expensetrackr/pkg/auth"
	"github.com/nutmos/expensetrackr/pkg/store"
	"github.com/nutmos/expensetrackr/pkg/user"

	"github.com/gin-gonic/gin"
)

// Authentication (issue #30): username + password, server-side sessions in
// a cookie. See docs/auth.md.

const sessionCookie = "session"

// touchEvery limits how often a session's sliding expiry is written back.
const touchEvery = time.Minute

// publicAPI are the /api routes reachable without a session.
var publicAPI = map[string]bool{
	"POST /api/auth/login":    true,
	"POST /api/auth/register": true, // the handler enforces the first-run rule
	"POST /api/auth/logout":   true, // idempotent; clears whatever cookie there is
	"GET /api/healthz":        true,
	"GET /api/openapi.yaml":   true,
	"GET /api/openapi.json":   true,
}

type authState struct {
	once     sync.Once
	throttle *auth.Throttle
	rate     *auth.RateLimiter
	bcrypt   *auth.Gate
	regMu    sync.Mutex
}

// defaultBcryptWait is how long a password request waits for a free bcrypt
// slot before it is answered 503 (Server.BcryptWait overrides it).
const defaultBcryptWait = 5 * time.Second

func (s *Server) authInit() {
	s.auth.once.Do(func() {
		s.auth.throttle = s.Throttle
		if s.auth.throttle == nil {
			s.auth.throttle = auth.NewThrottle()
		}
		s.auth.rate = s.LoginRateLimit
		if s.auth.rate == nil {
			s.auth.rate = auth.NewLoginRateLimiter()
		}
		s.auth.bcrypt = s.BcryptGate
		if s.auth.bcrypt == nil {
			s.auth.bcrypt = auth.NewGate(runtime.GOMAXPROCS(0) / 2)
		}
	})
}

// Input length limits for session handling.
const (
	maxSessionTokenLen = 128 // longest session cookie value worth looking up
	maxUserAgentLen    = 256 // user agent stored with a session is truncated to this
)

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Server) registerAuthRoutes(api *gin.RouterGroup) {
	g := api.Group("/auth")
	g.POST("/register", s.register)
	g.POST("/login", s.login)
	g.POST("/logout", s.logout)
	g.GET("/me", s.me)
	g.PUT("/password", s.changePassword)
}

// ---- middleware ------------------------------------------------------------

// csrfGuard rejects cross-site state-changing requests to /api: a
// Sec-Fetch-Site other than same-origin/none, or an Origin whose host is not
// the request's Host, gives 403. A request body must be application/json
// (415 otherwise), which a plain HTML form cannot send cross-site.
func csrfGuard(c *gin.Context) {
	switch c.Request.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		c.Next()
		return
	}
	if !strings.HasPrefix(c.Request.URL.Path, "/api/") {
		c.Next()
		return
	}
	// Bearer requests carry their credential explicitly; a browser never
	// adds it on its own and cannot send it cross-site without a CORS
	// preflight (never approved), and requireAuth ignores the cookie when a
	// Bearer header is present. So the Origin checks are skipped for them;
	// the JSON Content-Type rule below still applies.
	_, isBearer := bearerToken(c)
	if sfs := c.GetHeader("Sec-Fetch-Site"); !isBearer && sfs != "" && sfs != "same-origin" && sfs != "none" {
		c.AbortWithStatusJSON(http.StatusForbidden, errorBody{Error: "cross-site request rejected", Code: "csrf"})
		return
	}
	if o := c.GetHeader("Origin"); !isBearer && o != "" {
		u, err := url.Parse(o)
		if err != nil || o == "null" || !strings.EqualFold(u.Host, c.Request.Host) {
			c.AbortWithStatusJSON(http.StatusForbidden, errorBody{Error: "cross-origin request rejected", Code: "csrf"})
			return
		}
	}
	if c.Request.ContentLength != 0 && c.Request.Body != nil && c.Request.Body != http.NoBody {
		ct := strings.ToLower(strings.TrimSpace(strings.SplitN(c.GetHeader("Content-Type"), ";", 2)[0]))
		if ct != "application/json" {
			c.AbortWithStatusJSON(http.StatusUnsupportedMediaType, errorBody{Error: "Content-Type must be application/json"})
			return
		}
	}
	c.Next()
}

// requireAuth loads the session (if any) for every /api request and rejects
// non-public routes without one (401 JSON).
func (s *Server) requireAuth(c *gin.Context) {
	if tok, isBearer := bearerToken(c); isBearer {
		// A Bearer header decides alone; the cookie is ignored, so a token
		// request can never pick up session-only rights.
		if s.Store == nil {
			s.unauthorized(c)
			return
		}
		u, t, ok := s.loadAPIToken(c, tok)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, errorBody{Error: "invalid, expired or revoked API token", Code: "invalid_token"})
			return
		}
		if !tokenAllowed(c, t) {
			return
		}
		c.Set("user", u)
		c.Set("api_token", t)
		c.Next()
		return
	}
	if s.Store != nil {
		if u, se, ok := s.loadSession(c); ok {
			c.Set("user", u)
			c.Set("session", se)
		}
	}
	if publicAPI[c.Request.Method+" "+c.FullPath()] {
		c.Next()
		return
	}
	if _, ok := c.Get("user"); !ok {
		s.unauthorized(c)
		return
	}
	c.Next()
}

// unauthorized writes 401; code setup_required tells the web page to show
// first-run account creation instead of the login form.
func (s *Server) unauthorized(c *gin.Context) {
	body := errorBody{Error: "authentication required", Code: "unauthenticated"}
	if s.Store != nil {
		if has, err := s.Store.HasPasswordUser(c.Request.Context()); err == nil && !has {
			body.Code = "setup_required"
		}
	}
	c.AbortWithStatusJSON(http.StatusUnauthorized, body)
}

// loadSession validates the session cookie: known token, not expired, user
// active. It slides the expiry (at most once per touchEvery).
func (s *Server) loadSession(c *gin.Context) (user.User, store.Session, bool) {
	ck, err := c.Request.Cookie(sessionCookie)
	if err != nil || ck.Value == "" || len(ck.Value) > maxSessionTokenLen {
		return user.User{}, store.Session{}, false
	}
	ctx := c.Request.Context()
	se, err := s.Store.GetSession(ctx, auth.HashToken(ck.Value))
	if err != nil {
		return user.User{}, store.Session{}, false
	}
	now := s.now()
	if !now.Before(se.ExpiresAt) {
		_ = s.Store.DeleteSession(ctx, se.TokenHash)
		return user.User{}, store.Session{}, false
	}
	u, err := s.Store.GetUserByUID(ctx, se.UserUID)
	if err != nil || u.Status != user.StatusActive || !u.HasPassword {
		return user.User{}, store.Session{}, false
	}
	if now.Sub(se.LastSeenAt) >= touchEvery {
		se.LastSeenAt, se.ExpiresAt = now, now.Add(auth.SessionTTL)
		if s.Store.TouchSession(ctx, se.ID, se.LastSeenAt, se.ExpiresAt) == nil {
			s.setSessionCookie(c, ck.Value, se.ExpiresAt, now)
		}
	}
	return u, se, true
}

func (s *Server) setSessionCookie(c *gin.Context, tok string, exp, now time.Time) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name: sessionCookie, Value: tok, Path: "/",
		Expires: exp, MaxAge: int(exp.Sub(now).Seconds()),
		HttpOnly: true, Secure: s.isHTTPS(c), SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearSessionCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.isHTTPS(c), SameSite: http.SameSiteLaxMode,
	})
}

// startSession creates a session for u and sets the cookie.
func (s *Server) startSession(c *gin.Context, u user.User) error {
	tok, err := auth.NewToken()
	if err != nil {
		return err
	}
	now := s.now()
	ua := c.Request.UserAgent()
	if len(ua) > maxUserAgentLen {
		ua = ua[:maxUserAgentLen]
	}
	se := store.Session{TokenHash: auth.HashToken(tok), UserUID: u.UID, CreatedAt: now, ExpiresAt: now.Add(auth.SessionTTL), LastSeenAt: now, UserAgent: ua}
	ctx := c.Request.Context()
	_ = s.Store.DeleteExpiredSessions(ctx, now) // opportunistic cleanup
	if err := s.Store.CreateSession(ctx, &se); err != nil {
		return err
	}
	s.setSessionCookie(c, tok, se.ExpiresAt, now)
	return nil
}

func currentUser(c *gin.Context) (user.User, bool) {
	v, ok := c.Get("user")
	if !ok {
		return user.User{}, false
	}
	return v.(user.User), true
}

// ---- handlers ----------------------------------------------------------------

type credentials struct {
	Username    string  `json:"username"`
	Password    string  `json:"password"`
	DisplayName *string `json:"display_name"`
}

// register creates a user with a password. Open only while no user has a
// password (first-run setup); afterwards only a logged-in user may register
// others (403 otherwise). It never starts a session: the new user logs in
// with POST /api/auth/login using the credentials they chose (#165).
func (s *Server) register(c *gin.Context) {
	s.authInit()
	if s.rateLimited(c) {
		return
	}
	var in credentials
	if !decodeBody(c, &in, true) {
		return
	}
	fields := map[string]string{}
	username, msg := user.NormalizeUsername(in.Username)
	if msg != "" {
		fields["username"] = msg
	} else if username == "" {
		fields["username"] = "is required"
	}
	if msg := auth.CheckPasswordRules(in.Password); msg != "" {
		fields["password"] = msg
	}
	display := username
	if in.DisplayName != nil && strings.TrimSpace(*in.DisplayName) != "" {
		display = strings.TrimSpace(*in.DisplayName)
	}
	if len(display) > user.MaxDisplayNameLen {
		fields["display_name"] = fmt.Sprintf("must be at most %d characters", user.MaxDisplayNameLen)
	}
	_, loggedIn := currentUser(c)
	s.auth.regMu.Lock()
	defer s.auth.regMu.Unlock()
	if !loggedIn {
		has, err := s.Store.HasPasswordUser(c.Request.Context())
		if err != nil {
			internalError(c, err)
			return
		}
		if has {
			writeRegistrationClosed(c)
			return
		}
	}
	if len(fields) > 0 {
		c.JSON(http.StatusUnprocessableEntity, errorBody{Error: "validation failed", Fields: fields})
		return
	}
	if !s.acquireBcrypt(c) {
		return
	}
	hash, err := auth.HashPassword(in.Password)
	s.auth.bcrypt.Release()
	if err != nil {
		internalError(c, err)
		return
	}
	u := user.User{Username: &username, DisplayName: display}
	err = s.Store.RegisterUser(c.Request.Context(), &u, hash, !loggedIn)
	if errors.Is(err, store.ErrRegistrationClosed) {
		writeRegistrationClosed(c)
		return
	}
	if err != nil {
		writeUserError(c, err)
		return
	}
	u, _ = s.Store.GetUserByUID(c.Request.Context(), u.UID)
	c.JSON(http.StatusCreated, u)
}

const badLogin = "invalid username or password"

// rateLimited writes 429 with Retry-After and returns true when the client
// IP has used up its password-request budget (login and register share it).
// This caps the bcrypt work one client can cause, whatever usernames it
// tries (issue #155).
func (s *Server) rateLimited(c *gin.Context) bool {
	ok, wait := s.auth.rate.Allow(c.ClientIP())
	if ok {
		return false
	}
	c.Header("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
	c.JSON(http.StatusTooManyRequests, errorBody{Error: "too many requests; try again later", Code: "rate_limited"})
	return true
}

// acquireBcrypt takes a slot for bcrypt work, which is CPU-heavy by design.
// When every slot stays busy for defaultBcryptWait it writes 503 with Retry-After
// and returns false. On true the caller must s.auth.bcrypt.Release().
func (s *Server) acquireBcrypt(c *gin.Context) bool {
	wait := s.BcryptWait
	if wait <= 0 {
		wait = defaultBcryptWait
	}
	if s.auth.bcrypt.Acquire(c.Request.Context(), wait) == nil {
		return true
	}
	c.Header("Retry-After", "1")
	c.JSON(http.StatusServiceUnavailable, errorBody{Error: "server busy; try again shortly", Code: "busy"})
	return false
}

// throttled writes 429 with Retry-After and returns true when key is locked
// by the failed-attempt throttle.
func (s *Server) throttled(c *gin.Context, key string) bool {
	d := s.auth.throttle.Locked(key)
	if d <= 0 {
		return false
	}
	c.Header("Retry-After", strconv.Itoa(int(d.Seconds())+1))
	c.JSON(http.StatusTooManyRequests, errorBody{Error: "too many failed attempts; try again later", Code: "throttled"})
	return true
}

func (s *Server) login(c *gin.Context) {
	s.authInit()
	if s.rateLimited(c) {
		return
	}
	var in credentials
	if !decodeBody(c, &in, true) {
		return
	}
	username := strings.ToLower(strings.TrimSpace(in.Username))
	key := username + "|" + c.ClientIP()
	if s.throttled(c, key) {
		return
	}
	ctx := c.Request.Context()
	u, err := s.Store.GetUserByUsername(ctx, username)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		internalError(c, err)
		return
	}
	hash := ""
	if err == nil && u.PasswordHash != nil {
		hash = *u.PasswordHash
	}
	// Always runs one bcrypt comparison (dummy hash for unknown users).
	if !s.acquireBcrypt(c) {
		return
	}
	ok := auth.CheckPassword(hash, in.Password) && len(in.Password) <= auth.MaxPasswordLen
	s.auth.bcrypt.Release()
	if !ok || u.Status != user.StatusActive {
		s.auth.throttle.Fail(key)
		c.JSON(http.StatusUnauthorized, errorBody{Error: badLogin, Code: "invalid_credentials"})
		return
	}
	s.auth.throttle.Reset(key)
	// Replace any session this browser already had.
	if ck, err := c.Request.Cookie(sessionCookie); err == nil && ck.Value != "" {
		_ = s.Store.DeleteSession(ctx, auth.HashToken(ck.Value))
	}
	if err := s.startSession(c, u); err != nil {
		internalError(c, err)
		return
	}
	_ = s.Store.RecordLogin(ctx, u.UID, s.now())
	u, _ = s.Store.GetUserByUID(ctx, u.UID)
	c.JSON(http.StatusOK, u)
}

func (s *Server) logout(c *gin.Context) {
	if ck, err := c.Request.Cookie(sessionCookie); err == nil && ck.Value != "" && s.Store != nil {
		if err := s.Store.DeleteSession(c.Request.Context(), auth.HashToken(ck.Value)); err != nil {
			internalError(c, err)
			return
		}
	}
	s.clearSessionCookie(c)
	c.Status(http.StatusNoContent)
}

func (s *Server) me(c *gin.Context) {
	u, _ := currentUser(c)
	c.JSON(http.StatusOK, u)
}

type passwordChange struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// changePassword verifies the current password, stores the new hash and
// deletes every other session of the user (this one stays logged in).
func (s *Server) changePassword(c *gin.Context) {
	s.authInit()
	u, _ := currentUser(c)
	v, _ := c.Get("session")
	se := v.(store.Session)
	var in passwordChange
	if !decodeBody(c, &in, true) {
		return
	}
	if msg := auth.CheckPasswordRules(in.NewPassword); msg != "" {
		c.JSON(http.StatusUnprocessableEntity, errorBody{Error: "validation failed", Fields: map[string]string{"new_password": msg}})
		return
	}
	// Throttle current-password guesses per account, so a stolen session
	// cannot brute-force the password (same limits as login).
	key := "pwchange|" + u.UID
	if s.throttled(c, key) {
		return
	}
	hash := ""
	if u.PasswordHash != nil {
		hash = *u.PasswordHash
	}
	if !s.acquireBcrypt(c) {
		return
	}
	if !auth.CheckPassword(hash, in.CurrentPassword) {
		s.auth.bcrypt.Release()
		s.auth.throttle.Fail(key)
		c.JSON(http.StatusForbidden, errorBody{Error: "current password is incorrect",
			Fields: map[string]string{"current_password": "is incorrect"}})
		return
	}
	newHash, err := auth.HashPassword(in.NewPassword)
	s.auth.bcrypt.Release()
	if err != nil {
		internalError(c, err)
		return
	}
	if err := s.Store.ChangePassword(c.Request.Context(), u.UID, newHash, se.TokenHash); err != nil {
		writeUserError(c, err)
		return
	}
	s.auth.throttle.Reset(key)
	c.Status(http.StatusNoContent)
}
