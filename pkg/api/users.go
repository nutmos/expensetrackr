package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/nutmos/expensetrackr/pkg/store"
	"github.com/nutmos/expensetrackr/pkg/user"

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
}

// writeUserError maps user errors: 404, 409 duplicate username/email, then
// the shared 422/400/500 handling.
func writeUserError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		c.JSON(http.StatusNotFound, errorBody{Error: "user not found"})
	case errors.Is(err, store.ErrDuplicateUsername):
		c.JSON(http.StatusConflict, errorBody{Error: err.Error(),
			Fields: map[string]string{"username": "is already taken (usernames are case-insensitive)"}})
	case errors.Is(err, store.ErrDuplicateEmail):
		c.JSON(http.StatusConflict, errorBody{Error: err.Error(),
			Fields: map[string]string{"email": "is already used by another user"}})
	default:
		writeInputError(c, err)
	}
}

// resolveUser looks up /api/users/:uid (400 malformed, 404 unknown).
func (s *Server) resolveUser(c *gin.Context) (user.User, bool) {
	uid, ok := pathUID(c)
	if !ok {
		return user.User{}, false
	}
	u, err := s.Store.GetUserByUID(c.Request.Context(), uid)
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

// deleteUser removes the profile and its linked identities.
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
