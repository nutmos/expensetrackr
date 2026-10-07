package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode"

	"expense-service/internal/balance"
	"expense-service/internal/store"

	"github.com/gin-gonic/gin"
)

func (s *Server) registerBalanceRoutes(api *gin.RouterGroup) {
	api.POST("/balances", s.createBalance)
	api.GET("/balances", s.listBalances)
	api.GET("/balances/:id", s.getBalance)
	api.PUT("/balances/:id", s.replaceBalance)
	api.PATCH("/balances/:id", s.patchBalance)
	api.DELETE("/balances/:id", s.deleteBalance)
}

// writeBalanceError maps balance errors: 404 not found, 409 duplicate name,
// then the shared 422/400/500 handling.
func writeBalanceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		c.JSON(http.StatusNotFound, errorBody{Error: "balance not found"})
	case errors.Is(err, store.ErrDuplicateName):
		c.JSON(http.StatusConflict, errorBody{
			Error:  "a balance with this name already exists",
			Fields: map[string]string{"name": "is already used by another balance (names are case-insensitive)"},
		})
	default:
		writeInputError(c, err)
	}
}

// resolveBalance looks up /api/balances/:id. If the path segment is all digits
// it is treated as the numeric id; otherwise as the UUID uid. Returns the
// balance and true, or writes an error response and returns false.
func (s *Server) resolveBalance(c *gin.Context) (balance.Balance, bool) {
	raw := strings.TrimSpace(c.Param("id"))
	if raw == "" {
		c.JSON(http.StatusBadRequest, errorBody{Error: "id or uid is required"})
		return balance.Balance{}, false
	}
	var (
		b   balance.Balance
		err error
	)
	if isAllDigits(raw) {
		id, perr := strconv.ParseInt(raw, 10, 64)
		if perr != nil || id < 1 {
			c.JSON(http.StatusBadRequest, errorBody{Error: "id must be a positive integer"})
			return balance.Balance{}, false
		}
		b, err = s.Store.GetBalance(c.Request.Context(), id)
	} else {
		b, err = s.Store.GetBalanceByUID(c.Request.Context(), raw)
	}
	if err != nil {
		writeBalanceError(c, err)
		return balance.Balance{}, false
	}
	return b, true
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func (s *Server) createBalance(c *gin.Context) {
	var in balance.Input
	if !decodeBody(c, &in, true) {
		return
	}
	b, err := in.Validate()
	if err != nil {
		writeBalanceError(c, err)
		return
	}
	if err := s.Store.CreateBalance(c.Request.Context(), &b); err != nil {
		writeBalanceError(c, err)
		return
	}
	c.Header("Location", fmt.Sprintf("/api/balances/%d", b.ID))
	c.JSON(http.StatusCreated, b)
}

func (s *Server) listBalances(c *gin.Context) {
	typ := balance.Type(strings.ToLower(strings.TrimSpace(c.Query("type"))))
	if typ != "" && !typ.Valid() {
		names := make([]string, len(balance.Types))
		for i, t := range balance.Types {
			names[i] = string(t)
		}
		c.JSON(http.StatusBadRequest, errorBody{Error: "invalid query parameters",
			Fields: map[string]string{"type": "must be one of " + strings.Join(names, ", ")}})
		return
	}
	items, err := s.Store.ListBalances(c.Request.Context(), typ)
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"balances": items, "count": len(items), "totals": balance.ComputeTotals(items)})
}

func (s *Server) getBalance(c *gin.Context) {
	b, ok := s.resolveBalance(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, b)
}

// replaceBalance handles PUT: full replace with the same rules as create. A
// type change must come with the new type's amounts (and without the old ones).
// uid in the body is ignored; the existing uid is kept.
func (s *Server) replaceBalance(c *gin.Context) {
	cur, ok := s.resolveBalance(c)
	if !ok {
		return
	}
	var in balance.Input
	if !decodeBody(c, &in, true) {
		return
	}
	next, err := in.Validate()
	if err != nil {
		writeBalanceError(c, err)
		return
	}
	updated, err := s.Store.UpdateBalance(c.Request.Context(), cur.ID, func(balance.Balance) (balance.Balance, error) {
		return next, nil
	})
	if err != nil {
		writeBalanceError(c, err)
		return
	}
	c.JSON(http.StatusOK, updated)
}

// patchBalance handles PATCH: only fields present change; the merged result is
// validated as a whole (see balance.Input.ApplyPatch for type changes).
// A "uid" field in the body is ignored.
func (s *Server) patchBalance(c *gin.Context) {
	cur, ok := s.resolveBalance(c)
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
	updated, err := s.Store.UpdateBalance(c.Request.Context(), cur.ID, func(existing balance.Balance) (balance.Balance, error) {
		in := existing.Input()
		if err := in.ApplyPatch(patch); err != nil {
			return balance.Balance{}, err
		}
		return in.Validate()
	})
	if err != nil {
		writeBalanceError(c, err)
		return
	}
	c.JSON(http.StatusOK, updated)
}

func (s *Server) deleteBalance(c *gin.Context) {
	cur, ok := s.resolveBalance(c)
	if !ok {
		return
	}
	if err := s.Store.DeleteBalance(c.Request.Context(), cur.ID); err != nil {
		writeBalanceError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
