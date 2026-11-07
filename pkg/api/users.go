package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/nutmos/expensetrackr/pkg/user"
	"github.com/nutmos/expensetrackr/pkg/validate"

	"github.com/gin-gonic/gin"
)

// User profiles. Like the rest of the API these endpoints require a login
// (see auth.go); they never accept or return passwords/hashes (passwords are
// set via /api/auth) and expose linked SSO identities read-only.
//
// Each user sees and manages only their own account (issue #76): another
// user's uid answers 404 exactly like an unknown one, GET /api/users lists
// only the caller, and POST /api/users is disabled (accounts are created with
// POST /api/auth/register). Changing or deleting the account needs a browser
// session (sessionOnlyAPI in tokens.go); API tokens can read it and change
// preferences.
func (s *Server) registerUserRoutes(api *gin.RouterGroup) {
	api.POST("/users", s.createUser)
	api.GET("/users", s.listUsers)
	api.GET("/users/:uid", s.getUser)
	api.PUT("/users/:uid", s.replaceUser)
	api.PATCH("/users/:uid", s.patchUser)
	api.DELETE("/users/:uid", s.deleteUser)
	api.GET("/users/:uid/identities", s.listUserIdentities)
	api.GET("/users/:uid/preferences", s.getPreferences)
	api.PUT("/users/:uid/preferences", s.replacePreferences)
}

// resolveOwnUser looks up /api/users/:uid for routes a user may only use on
// their own account: 400 if :uid is malformed, 404 if it is unknown or
// belongs to someone else (the same answer, so uids of other accounts can't
// be probed).
func (s *Server) resolveOwnUser(c *gin.Context) (user.User, bool) {
	uid, ok := pathUID(c)
	if !ok {
		return user.User{}, false
	}
	if me, ok := currentUser(c); !ok || !uid.Matches(me.UID) {
		writeNotFound(c, "user")
		return user.User{}, false
	}
	u, err := s.Store.GetUserByUID(c.Request.Context(), uid.String())
	if err != nil {
		writeUserError(c, err)
		return user.User{}, false
	}
	return u, true
}

// createUser answers 403: with no admin role, a profile made this way could
// never log in and nobody could manage it (issue #76). Accounts are created
// with POST /api/auth/register.
func (s *Server) createUser(c *gin.Context) {
	c.JSON(http.StatusForbidden, errorBody{
		Error: "creating user profiles here is disabled; create an account with POST /api/auth/register",
		Code:  "use_register",
	})
}

// listUsers returns only the caller's own account.
func (s *Server) listUsers(c *gin.Context) {
	me, ok := currentUser(c)
	if !ok {
		s.unauthorized(c)
		return
	}
	u, err := s.Store.GetUserByUID(c.Request.Context(), me.UID)
	if err != nil {
		writeUserError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"users": []user.User{u}, "count": 1})
}

func (s *Server) getUser(c *gin.Context) {
	if u, ok := s.resolveOwnUser(c); ok {
		c.JSON(http.StatusOK, u)
	}
}

// replaceUser handles PUT: full replace of the profile fields (display_name
// required; omitted username/email are removed; omitted preferences become {}).
func (s *Server) replaceUser(c *gin.Context) {
	cur, ok := s.resolveOwnUser(c)
	if !ok {
		return
	}
	var in user.Input
	if !decodeBody(c, &in, true) {
		return
	}
	next, err := in.Validate()
	if err != nil {
		writeUserError(c, err)
		return
	}
	updated, err := s.Store.UpdateUser(c.Request.Context(), cur.ID, func(user.User) (user.User, error) { return next, nil })
	if err != nil {
		writeUserError(c, err)
		return
	}
	c.JSON(http.StatusOK, updated)
}

// patchUser handles PATCH: only fields present change; "preferences"
// replaces the whole object (see user.Input.ApplyPatch).
func (s *Server) patchUser(c *gin.Context) {
	cur, ok := s.resolveOwnUser(c)
	if !ok {
		return
	}
	var patch map[string]json.RawMessage
	if !decodeBody(c, &patch, false) {
		return
	}
	if patch == nil {
		c.JSON(http.StatusBadRequest, errorBody{Error: "request body must be a JSON object"})
		return
	}
	updated, err := s.Store.UpdateUser(c.Request.Context(), cur.ID, func(existing user.User) (user.User, error) {
		in := existing.Input()
		if err := in.ApplyPatch(patch); err != nil {
			return user.User{}, err
		}
		return in.Validate()
	})
	if err != nil {
		writeUserError(c, err)
		return
	}
	c.JSON(http.StatusOK, updated)
}

// deleteUser removes the profile and its linked identities. The last user
// who can log in cannot be deleted (409 last_login_user).
func (s *Server) deleteUser(c *gin.Context) {
	cur, ok := s.resolveOwnUser(c)
	if !ok {
		return
	}
	if err := s.Store.DeleteUser(c.Request.Context(), cur.ID); err != nil {
		writeUserError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// listUserIdentities is read-only: identities are created by the future SSO
// sign-in flow, not through the API.
func (s *Server) listUserIdentities(c *gin.Context) {
	u, ok := s.resolveOwnUser(c)
	if !ok {
		return
	}
	items, err := s.Store.ListIdentities(c.Request.Context(), u.UID)
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"identities": items, "count": len(items)})
}

// getPreferences returns the effective preferences (defaults filled in).
func (s *Server) getPreferences(c *gin.Context) {
	if u, ok := s.resolveOwnUser(c); ok {
		c.JSON(http.StatusOK, u.Preferences)
	}
}

// replacePreferences handles PUT: the body replaces the whole preferences
// object (omitted keys go back to their defaults). Any invalid value,
// including an unsupported base_currency or an unknown key, is a 400.
func (s *Server) replacePreferences(c *gin.Context) {
	cur, ok := s.resolveOwnUser(c)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBodyBytes)
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, errorBody{Error: "request body too large"})
		return
	}
	if !json.Valid(raw) {
		c.JSON(http.StatusBadRequest, errorBody{Error: "request body must be a JSON object"})
		return
	}
	prefs, err := user.DecodePreferences(raw)
	var ve *validate.ValidationError
	if errors.As(err, &ve) {
		c.JSON(http.StatusBadRequest, errorBody{Error: "invalid preferences", Fields: ve.Fields})
		return
	} else if err != nil {
		internalError(c, err)
		return
	}
	updated, err := s.Store.UpdateUser(c.Request.Context(), cur.ID, func(existing user.User) (user.User, error) {
		existing.Preferences = prefs
		return existing, nil
	})
	if err != nil {
		writeUserError(c, err)
		return
	}
	c.JSON(http.StatusOK, updated.Preferences)
}
