package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/nutmos/expensetrackr/pkg/category"
	"github.com/nutmos/expensetrackr/pkg/store"

	"github.com/gin-gonic/gin"
)

func (s *Server) registerCategoryRoutes(api *gin.RouterGroup) {
	api.POST("/categories", s.createCategory)
	api.GET("/categories", s.listCategories)
	api.GET("/categories/:uid", s.getCategory)
	api.PUT("/categories/:uid", s.replaceCategory)
	api.PATCH("/categories/:uid", s.patchCategory)
	api.DELETE("/categories/:uid", s.deleteCategory)
}

// writeCategoryError maps category errors: 404 not found, 409 duplicate name
// or in use, then the shared 422/400/500 handling.
func writeCategoryError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		c.JSON(http.StatusNotFound, errorBody{Error: "category not found"})
	case errors.Is(err, store.ErrDuplicateCategory):
		c.JSON(http.StatusConflict, errorBody{
			Error:  "a category with this name and type already exists",
			Fields: map[string]string{"name": "is already used by another category of this type (names are case-insensitive)"},
		})
	case errors.Is(err, store.ErrCategoryInUse):
		c.JSON(http.StatusConflict, errorBody{Error: err.Error() +
			"; reassign or clear category_uid on those transactions first"})
	default:
		writeInputError(c, err)
	}
}

// resolveCategory looks up /api/categories/:uid (400 malformed, 404 unknown).
func (s *Server) resolveCategory(c *gin.Context) (category.Category, bool) {
	uid, ok := pathUID(c)
	if !ok {
		return category.Category{}, false
	}
	cat, err := s.Store.GetCategoryByUID(c.Request.Context(), uid)
	if err != nil {
		writeCategoryError(c, err)
		return category.Category{}, false
	}
	return cat, true
}

func (s *Server) createCategory(c *gin.Context) {
	var in category.Input
	if !decodeBody(c, &in, true) {
		return
	}
	cat, err := in.Validate()
	if err != nil {
		writeCategoryError(c, err)
		return
	}
	if err := s.Store.CreateCategory(c.Request.Context(), &cat); err != nil {
		writeCategoryError(c, err)
		return
	}
	c.Header("Location", "/api/categories/"+cat.UID)
	c.JSON(http.StatusCreated, cat)
}

func (s *Server) listCategories(c *gin.Context) {
	var typ category.Type
	if raw := strings.TrimSpace(c.Query("type")); raw != "" {
		t, ok := category.ParseType(raw)
		if !ok {
			c.JSON(http.StatusBadRequest, errorBody{Error: "invalid query parameters",
				Fields: map[string]string{"type": "must be one of: expense, income"}})
			return
		}
		typ = t
	}
	items, err := s.Store.ListCategories(c.Request.Context(), typ)
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"categories": items, "count": len(items)})
}

func (s *Server) getCategory(c *gin.Context) {
	if cat, ok := s.resolveCategory(c); ok {
		c.JSON(http.StatusOK, cat)
	}
}

// replaceCategory handles PUT (full replace; omitted description clears it).
func (s *Server) replaceCategory(c *gin.Context) {
	cur, ok := s.resolveCategory(c)
	if !ok {
		return
	}
	var in category.Input
	if !decodeBody(c, &in, true) {
		return
	}
	next, err := in.Validate()
	if err != nil {
		writeCategoryError(c, err)
		return
	}
	updated, err := s.Store.UpdateCategory(c.Request.Context(), cur.ID, func(category.Category) (category.Category, error) {
		return next, nil
	})
	if err != nil {
		writeCategoryError(c, err)
		return
	}
	c.JSON(http.StatusOK, updated)
}

// patchCategory handles PATCH (only fields present change).
func (s *Server) patchCategory(c *gin.Context) {
	cur, ok := s.resolveCategory(c)
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
	updated, err := s.Store.UpdateCategory(c.Request.Context(), cur.ID, func(existing category.Category) (category.Category, error) {
		in := existing.Input()
		if err := in.ApplyPatch(patch); err != nil {
			return category.Category{}, err
		}
		return in.Validate()
	})
	if err != nil {
		writeCategoryError(c, err)
		return
	}
	c.JSON(http.StatusOK, updated)
}

// deleteCategory handles DELETE; 409 if any transaction references it.
func (s *Server) deleteCategory(c *gin.Context) {
	cur, ok := s.resolveCategory(c)
	if !ok {
		return
	}
	if err := s.Store.DeleteCategory(c.Request.Context(), cur.ID); err != nil {
		writeCategoryError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
