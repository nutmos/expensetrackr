package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

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
	id, ok := parseID(c)
	if !ok {
		return
	}
	b, err := s.Store.GetBalance(c.Request.Context(), id)
	if err != nil {
		writeBalanceError(c, err)
		return
	}
	c.JSON(http.StatusOK, b)
}

// replaceBalance handles PUT: full replace with the same rules as create. A
// type change must come with the new type's amounts (and without the old ones).
func (s *Server) replaceBalance(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var in balance.Input
	if !decodeBody(c, &in, true) {
		return
	}
	next, err := in.Validate()
	if err != nil {
		if _, gerr := s.Store.GetBalance(c.Request.Context(), id); errors.Is(gerr, store.ErrNotFound) {
			writeBalanceError(c, gerr)
			return
		}
		writeBalanceError(c, err)
		return
	}
	updated, err := s.Store.UpdateBalance(c.Request.Context(), id, func(balance.Balance) (balance.Balance, error) {
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
func (s *Server) patchBalance(c *gin.Context) {
	id, ok := parseID(c)
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
	updated, err := s.Store.UpdateBalance(c.Request.Context(), id, func(cur balance.Balance) (balance.Balance, error) {
		in := cur.Input()
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
	id, ok := parseID(c)
	if !ok {
		return
	}
	if err := s.Store.DeleteBalance(c.Request.Context(), id); err != nil {
		writeBalanceError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
