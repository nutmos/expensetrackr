package api

import (
	"errors"
	"net/http"
	"net/url"
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
	regMu    sync.Mutex
}

func (s *Server) authInit() {
	s.auth.once.Do(func() {
		if s.Throttle != nil {
			s.auth.throttle = s.Throttle
		} else {
			s.auth.throttle = auth.NewThrottle()
		}
	})
}

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
	if sfs := c.GetHeader("Sec-Fetch-Site"); sfs != "" && sfs != "same-origin" && sfs != "none" {
		c.AbortWithStatusJSON(http.StatusForbidden, errorBody{Error: "cross-site request rejected", Code: "csrf"})
		return
	}
	if o := c.GetHeader("Origin"); o != "" {
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
	if err != nil || ck.Value == "" || len(ck.Value) > 128 {
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
			setSessionCookie(c, ck.Value, se.ExpiresAt, now)
		}
	}
	return u, se, true
}

func setSessionCookie(c *gin.Context, tok string, exp, now time.Time) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name: sessionCookie, Value: tok, Path: "/",
		Expires: exp, MaxAge: int(exp.Sub(now).Seconds()),
		HttpOnly: true, Secure: c.Request.TLS != nil, SameSite: http.SameSiteLaxMode,
	})
}

func clearSessionCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: c.Request.TLS != nil, SameSite: http.SameSiteLaxMode,
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
	if len(ua) > 256 {
		ua = ua[:256]
	}
	se := store.Session{TokenHash: auth.HashToken(tok), UserUID: u.UID, CreatedAt: now, ExpiresAt: now.Add(auth.SessionTTL), LastSeenAt: now, UserAgent: ua}
	ctx := c.Request.Context()
	_ = s.Store.DeleteExpiredSessions(ctx, now) // opportunistic cleanup
	if err := s.Store.CreateSession(ctx, &se); err != nil {
		return err
	}
	setSessionCookie(c, tok, se.ExpiresAt, now)
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
// password (first-run setup; the new user is logged in); afterwards only a
// logged-in user may register others (403 otherwise).
func (s *Server) register(c *gin.Context) {
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
	if len(display) > 100 {
		fields["display_name"] = "must be at most 100 characters"
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
			c.JSON(http.StatusForbidden, errorBody{Error: "registration is closed; log in to create more users", Code: "registration_closed"})
			return
		}
	}
	if len(fields) > 0 {
		c.JSON(http.StatusUnprocessableEntity, errorBody{Error: "validation failed", Fields: fields})
		return
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		internalError(c, err)
		return
	}
	u := user.User{Username: &username, DisplayName: display, Preferences: []byte("{}")}
	err = s.Store.RegisterUser(c.Request.Context(), &u, hash, !loggedIn)
	switch {
	case errors.Is(err, store.ErrRegistrationClosed):
		c.JSON(http.StatusForbidden, errorBody{Error: "registration is closed; log in to create more users", Code: "registration_closed"})
		return
	case err != nil:
		writeUserError(c, err)
		return
	}
	if !loggedIn {
		if err := s.startSession(c, u); err != nil {
			internalError(c, err)
			return
		}
		_ = s.Store.RecordLogin(c.Request.Context(), u.UID, s.now())
	}
	u, _ = s.Store.GetUserByUID(c.Request.Context(), u.UID)
	c.JSON(http.StatusCreated, u)
}

const badLogin = "invalid username or password"

func (s *Server) login(c *gin.Context) {
	s.authInit()
	var in credentials
	if !decodeBody(c, &in, true) {
		return
	}
	username := strings.ToLower(strings.TrimSpace(in.Username))
	key := username + "|" + c.ClientIP()
	if d := s.auth.throttle.Locked(key); d > 0 {
		c.Header("Retry-After", strconv.Itoa(int(d.Seconds())+1))
		c.JSON(http.StatusTooManyRequests, errorBody{Error: "too many failed attempts; try again later", Code: "throttled"})
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
	ok := auth.CheckPassword(hash, in.Password) && len(in.Password) <= auth.MaxPasswordLen
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
	clearSessionCookie(c)
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
	hash := ""
	if u.PasswordHash != nil {
		hash = *u.PasswordHash
	}
	if !auth.CheckPassword(hash, in.CurrentPassword) {
		c.JSON(http.StatusForbidden, errorBody{Error: "current password is incorrect",
			Fields: map[string]string{"current_password": "is incorrect"}})
		return
	}
	newHash, err := auth.HashPassword(in.NewPassword)
	if err != nil {
		internalError(c, err)
		return
	}
	if err := s.Store.ChangePassword(c.Request.Context(), u.UID, newHash, se.TokenHash); err != nil {
		writeUserError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
