package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/nutmos/expensetrackr/pkg/balance"
	"github.com/nutmos/expensetrackr/pkg/store"

	"github.com/gin-gonic/gin"
)

func (s *Server) registerBalanceRoutes(api *gin.RouterGroup) {
	api.POST("/balances", s.createBalance)
	api.GET("/balances", s.listBalances)
	api.GET("/balances/:uid", s.getBalance)
	api.PUT("/balances/:uid", s.replaceBalance)
	api.PATCH("/balances/:uid", s.patchBalance)
	api.DELETE("/balances/:uid", s.deleteBalance)
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

// resolveBalance looks up /api/balances/:uid. Only the UUID uid is accepted:
// a malformed value is 400, an unknown uid is 404.
func (s *Server) resolveBalance(c *gin.Context) (balance.Balance, bool) {
	uid, ok := pathUID(c)
	if !ok {
		return balance.Balance{}, false
	}
	b, err := s.Store.GetBalanceByUID(c.Request.Context(), uid)
	if err != nil {
		writeBalanceError(c, err)
		return balance.Balance{}, false
	}
	return b, true
}

// pathUID reads the :uid path parameter, lowercases it and checks it has the
// canonical UUID shape (8-4-4-4-12 hex). Writes 400 and returns false if not.
func pathUID(c *gin.Context) (string, bool) {
	uid := strings.ToLower(strings.TrimSpace(c.Param("uid")))
	if !uuidRE.MatchString(uid) {
		c.JSON(http.StatusBadRequest, errorBody{Error: "uid must be a UUID (xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx)"})
		return "", false
	}
	return uid, true
}

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

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
	c.Header("Location", "/api/balances/"+b.UID)
	c.JSON(http.StatusCreated, b)
}

func (s *Server) listBalances(c *gin.Context) {
	typ := balance.Type(strings.ToLower(strings.TrimSpace(c.Query("type"))))
	payableOnly := strings.EqualFold(c.Query("payable"), "1") || strings.EqualFold(c.Query("payable"), "true")
	if typ != "" && payableOnly {
		c.JSON(http.StatusBadRequest, errorBody{Error: "invalid query parameters",
			Fields: map[string]string{"payable": "cannot be combined with type"}})
		return
	}
	if typ != "" && !typ.Valid() {
		names := make([]string, len(balance.Types))
		for i, t := range balance.Types {
			names[i] = string(t)
		}
		c.JSON(http.StatusBadRequest, errorBody{Error: "invalid query parameters",
			Fields: map[string]string{"type": "must be one of " + strings.Join(names, ", ")}})
		return
	}
	var (
		items []balance.Balance
		err   error
	)
	if payableOnly {
		items, err = s.Store.ListPayableBalances(c.Request.Context())
	} else {
		items, err = s.Store.ListBalances(c.Request.Context(), typ)
	}
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

// replaceBalance handles PUT: full replace with the same rules as create,
// except that type is immutable: it is still required and must equal the
// stored type (422 on "type" otherwise). uid in the body is ignored; the
// existing uid is kept.
func (s *Server) replaceBalance(c *gin.Context) {
	cur, ok := s.resolveBalance(c)
	if !ok {
		return
	}
	var in balance.Input
	if !decodeBody(c, &in, true) {
		return
	}
	updated, err := s.Store.UpdateBalance(c.Request.Context(), cur.ID, func(existing balance.Balance) (balance.Balance, error) {
		return in.ValidateUpdate(existing.Type)
	})
	if err != nil {
		writeBalanceError(c, err)
		return
	}
	c.JSON(http.StatusOK, updated)
}

// patchBalance handles PATCH: only fields present change; the merged result is
// validated as a whole. "type" may be sent only with the stored value.
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
		return in.ValidateUpdate(existing.Type)
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
