package api

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/nutmos/expensetrackr/pkg/auth"
	"github.com/nutmos/expensetrackr/pkg/store"
	"github.com/nutmos/expensetrackr/pkg/user"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Personal API tokens: `Authorization: Bearer ext_...` as an alternative to
// the session cookie, for scripts and AI agents. See docs/auth.md.

const (
	// ScopeRead allows GET (and HEAD) requests.
	ScopeRead = "read"
	// ScopeWrite allows every method.
	ScopeWrite = "write"

	// MaxTokenNameLen is the longest token name accepted.
	MaxTokenNameLen = 64
	// MaxTokenExpiryDays is the longest expires_in_days accepted.
	MaxTokenExpiryDays = 3650
	// maxAPITokenLen is the longest Bearer value worth looking up.
	maxAPITokenLen = 128

	bearerScheme = "Bearer "
	hoursPerDay  = 24
)

// allowedScopes are the scopes a token may carry.
var allowedScopes = []string{ScopeRead, ScopeWrite}

// sessionOnlyAPI are routes that need a cookie session: managing tokens,
// changing credentials or the account (the username is the login name),
// deleting it, and logging out. A token gets 403 there.
var sessionOnlyAPI = map[string]bool{
	"GET /api/tokens":         true,
	"POST /api/tokens":        true,
	"DELETE /api/tokens/:uid": true,
	"PUT /api/auth/password":  true,
	"POST /api/auth/logout":   true,
	"POST /api/auth/register": true, // creates accounts with passwords
	"PUT /api/users/:uid":     true, // issue #76
	"PATCH /api/users/:uid":   true,
	"DELETE /api/users/:uid":  true,
}

func (s *Server) registerTokenRoutes(api *gin.RouterGroup) {
	api.GET("/tokens", s.listTokens)
	api.POST("/tokens", s.createToken)
	api.DELETE("/tokens/:uid", s.revokeToken)
}

// bearerToken returns the token from an Authorization: Bearer header and
// whether such a header was sent at all.
func bearerToken(c *gin.Context) (string, bool) {
	h := c.GetHeader("Authorization")
	if len(h) < len(bearerScheme) || !strings.EqualFold(h[:len(bearerScheme)], bearerScheme) {
		return "", false
	}
	return strings.TrimSpace(h[len(bearerScheme):]), true
}

// loadAPIToken validates a Bearer token: known hash, not revoked, not
// expired, owner active. last_used_at is written at most once per touchEvery.
func (s *Server) loadAPIToken(c *gin.Context, tok string) (user.User, store.APIToken, bool) {
	if len(tok) > maxAPITokenLen || !strings.HasPrefix(tok, auth.APITokenPrefix) {
		return user.User{}, store.APIToken{}, false
	}
	ctx := c.Request.Context()
	hash := auth.HashToken(tok)
	t, err := s.Store.GetAPITokenByHash(ctx, hash)
	if err != nil || subtle.ConstantTimeCompare([]byte(t.TokenHash), []byte(hash)) != 1 {
		return user.User{}, store.APIToken{}, false
	}
	now := s.now()
	if t.RevokedAt != nil {
		return user.User{}, store.APIToken{}, false
	} else if t.ExpiresAt != nil && !now.Before(*t.ExpiresAt) {
		return user.User{}, store.APIToken{}, false
	}
	u, err := s.Store.GetUserByUID(ctx, t.UserUID)
	if err != nil || u.Status != user.StatusActive || !u.HasPassword {
		return user.User{}, store.APIToken{}, false
	}
	if t.LastUsedAt == nil || now.Sub(*t.LastUsedAt) >= touchEvery {
		if s.Store.TouchAPIToken(ctx, t.ID, now) == nil {
			t.LastUsedAt = &now
		}
	}
	return u, t, true
}

// tokenAllowed enforces what a token request may do; it writes 403 and
// returns false otherwise.
func tokenAllowed(c *gin.Context, t store.APIToken) bool {
	route := c.Request.Method + " " + c.FullPath()
	m := c.Request.Method
	if sessionOnlyAPI[route] {
		c.AbortWithStatusJSON(http.StatusForbidden, errorBody{Error: "this endpoint requires a browser session; API tokens cannot use it", Code: "session_required"})
		return false
	} else if t.HasScope(ScopeWrite) {
		return true
	} else if t.HasScope(ScopeRead) && (m == http.MethodGet || m == http.MethodHead) {
		return true
	}
	c.AbortWithStatusJSON(http.StatusForbidden, errorBody{Error: "the API token's scopes do not allow this request", Code: "insufficient_scope"})
	return false
}

// ---- handlers ----------------------------------------------------------------

type tokenInput struct {
	Name          string   `json:"name"`
	Scopes        []string `json:"scopes"`
	ExpiresInDays *int     `json:"expires_in_days"`
}

// createdToken is the POST /api/tokens response: the stored token plus the
// plaintext, which is never returned again.
type createdToken struct {
	store.APIToken
	Token string `json:"token"`
}

func (s *Server) listTokens(c *gin.Context) {
	u, _ := currentUser(c)
	list, err := s.Store.ListAPITokens(c.Request.Context(), u.UID)
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"tokens": list, "count": len(list)})
}

func validateTokenInput(in tokenInput) (name string, scopes []string, fields map[string]string) {
	fields = map[string]string{}
	name = strings.TrimSpace(in.Name)
	if name == "" {
		fields["name"] = "is required"
	} else if len([]rune(name)) > MaxTokenNameLen {
		fields["name"] = fmt.Sprintf("must be at most %d characters", MaxTokenNameLen)
	}
	for _, sc := range in.Scopes {
		if !slices.Contains(allowedScopes, sc) {
			fields["scopes"] = fmt.Sprintf("unknown scope %q; allowed: %s", sc, strings.Join(allowedScopes, ", "))
			break
		}
		if !slices.Contains(scopes, sc) {
			scopes = append(scopes, sc)
		}
	}
	if len(in.Scopes) == 0 {
		fields["scopes"] = "at least one scope is required"
	}
	if d := in.ExpiresInDays; d != nil && (*d < 1 || *d > MaxTokenExpiryDays) {
		fields["expires_in_days"] = fmt.Sprintf("must be between 1 and %d", MaxTokenExpiryDays)
	}
	slices.Sort(scopes)
	return name, scopes, fields
}

func (s *Server) createToken(c *gin.Context) {
	u, _ := currentUser(c)
	var in tokenInput
	if !decodeBody(c, &in, true) {
		return
	}
	name, scopes, fields := validateTokenInput(in)
	if len(fields) > 0 {
		c.JSON(http.StatusUnprocessableEntity, errorBody{Error: "validation failed", Fields: fields})
		return
	}
	plain, prefix, err := auth.NewAPIToken()
	if err != nil {
		internalError(c, err)
		return
	}
	t := store.APIToken{UserUID: u.UID, Name: name, Prefix: prefix, TokenHash: auth.HashToken(plain), Scopes: scopes}
	if in.ExpiresInDays != nil {
		exp := s.now().Add(time.Duration(*in.ExpiresInDays) * hoursPerDay * time.Hour).UTC().Truncate(time.Second)
		t.ExpiresAt = &exp
	}
	if err := s.Store.CreateAPIToken(c.Request.Context(), &t); err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusCreated, createdToken{APIToken: t, Token: plain})
}

func (s *Server) revokeToken(c *gin.Context) {
	u, _ := currentUser(c)
	id, err := uuid.Parse(c.Param("uid"))
	if err != nil {
		c.JSON(http.StatusBadRequest, errorBody{Error: "token uid must be a UUID"})
		return
	}
	err = s.Store.RevokeAPIToken(c.Request.Context(), u.UID, id.String())
	if errors.Is(err, store.ErrTokenNotFound) {
		c.JSON(http.StatusNotFound, errorBody{Error: "token not found"})
		return
	} else if err != nil {
		internalError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
