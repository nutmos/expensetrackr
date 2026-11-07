package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/nutmos/expensetrackr/pkg/store"
	"github.com/nutmos/expensetrackr/pkg/user"
	"github.com/nutmos/expensetrackr/pkg/validate"

	"github.com/gin-gonic/gin"
)

// User profiles. Like the rest of the API these endpoints require a session
// (see auth.go); they never accept or return passwords/hashes (passwords are
// set via /api/auth) and expose linked SSO identities read-only.
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

// writeUserError maps user errors: 404, 409 duplicate username/email or last
// login account, 422 username required, then the shared 422/400/500 handling.
func writeUserError(c *gin.Context, err error) {
	if errors.Is(err, store.ErrNotFound) {
		c.JSON(http.StatusNotFound, errorBody{Error: "user not found"})
	} else if errors.Is(err, store.ErrDuplicateUsername) {
		c.JSON(http.StatusConflict, errorBody{Error: err.Error(),
			Fields: map[string]string{"username": "is already taken (usernames are case-insensitive)"}})
	} else if errors.Is(err, store.ErrDuplicateEmail) {
		c.JSON(http.StatusConflict, errorBody{Error: err.Error(),
			Fields: map[string]string{"email": "is already used by another user"}})
	} else if errors.Is(err, store.ErrUsernameRequired) {
		c.JSON(http.StatusUnprocessableEntity, errorBody{Error: "validation failed", Code: "username_required",
			Fields: map[string]string{"username": "cannot be removed: this user logs in with it"}})
	} else if errors.Is(err, store.ErrLastPasswordUser) {
		c.JSON(http.StatusConflict, errorBody{Error: err.Error(), Code: "last_login_user"})
	} else {
		writeInputError(c, err)
	}
}

// resolveUser looks up /api/users/:uid (400 malformed, 404 unknown).
func (s *Server) resolveUser(c *gin.Context) (user.User, bool) {
	uid, ok := pathUID(c)
	if !ok {
		return user.User{}, false
	}
	u, err := s.Store.GetUserByUID(c.Request.Context(), uid.String())
	if err != nil {
		writeUserError(c, err)
		return user.User{}, false
	}
	return u, true
}

func (s *Server) createUser(c *gin.Context) {
	var in user.Input
	if !decodeBody(c, &in, true) {
		return
	}
	u, err := in.Validate()
	if err != nil {
		writeUserError(c, err)
		return
	}
	if err := s.Store.CreateUser(c.Request.Context(), &u); err != nil {
		writeUserError(c, err)
		return
	}
	c.Header("Location", "/api/users/"+u.UID)
	c.JSON(http.StatusCreated, u)
}

func (s *Server) listUsers(c *gin.Context) {
	items, err := s.Store.ListUsers(c.Request.Context())
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"users": items, "count": len(items)})
}

func (s *Server) getUser(c *gin.Context) {
	if u, ok := s.resolveUser(c); ok {
		c.JSON(http.StatusOK, u)
	}
}

// replaceUser handles PUT: full replace of the profile fields (display_name
// required; omitted username/email are removed; omitted preferences become {}).
func (s *Server) replaceUser(c *gin.Context) {
	cur, ok := s.resolveUser(c)
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
	cur, ok := s.resolveUser(c)
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
	cur, ok := s.resolveUser(c)
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
	u, ok := s.resolveUser(c)
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

// resolveOwnUser is resolveUser for endpoints only the user themself may use:
// another user's uid is 403 (preferences are personal).
func (s *Server) resolveOwnUser(c *gin.Context) (user.User, bool) {
	u, ok := s.resolveUser(c)
	if !ok {
		return user.User{}, false
	}
	if me, ok := currentUser(c); !ok || me.UID != u.UID {
		c.JSON(http.StatusForbidden, errorBody{Error: "preferences can only be read or changed by their own user"})
		return user.User{}, false
	}
	return u, true
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
