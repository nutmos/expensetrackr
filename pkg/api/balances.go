package api

import (
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
	api.DELETE("/balances/:uid", s.deleteBalance)
}

// writeBalanceError maps balance errors: 404 not found, 409 duplicate name or
// currency change while in use, then the shared 422/400/500 handling.
// (Version conflicts are answered by balanceConflict, which needs the store.)
func writeBalanceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		c.JSON(http.StatusNotFound, errorBody{Error: "balance not found"})
	case errors.Is(err, store.ErrBalanceInUse):
		c.JSON(http.StatusConflict, errorBody{
			Error:  err.Error(),
			Code:   "balance_in_use",
			Fields: map[string]string{"currency": "cannot change while transactions reference this balance (no exchange-rate conversion)"},
		})
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
	setETag(c, b.Version)
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
	setETag(c, b.Version)
	c.JSON(http.StatusOK, b)
}

// balanceConflict answers 409 with the balance as it is now (or 404 if it was
// deleted meanwhile).
func (s *Server) balanceConflict(c *gin.Context, uid string) {
	cur, err := s.Store.GetBalanceByUID(c.Request.Context(), uid)
	if err != nil {
		writeBalanceError(c, err)
		return
	}
	writeVersionConflict(c, "balance", cur, cur.Version)
}

// saveBalance runs a manual update with optimistic locking and writes the
// response: 200 + ETag, 409 on a stale version, or the mapped error.
func (s *Server) saveBalance(c *gin.Context, cur balance.Balance, opts store.WriteOptions, fn func(existing balance.Balance) (balance.Balance, error)) {
	if cur.Version != opts.Version { // fail fast; re-checked inside the write
		s.balanceConflict(c, cur.UID)
		return
	}
	updated, err := s.Store.UpdateBalanceWith(c.Request.Context(), cur.ID, opts, fn)
	if errors.Is(err, store.ErrVersionConflict) {
		s.balanceConflict(c, cur.UID)
		return
	}
	if err != nil {
		writeBalanceError(c, err)
		return
	}
	setETag(c, updated.Version)
	c.JSON(http.StatusOK, updated)
}

// replaceBalance handles PUT: full replace with the same rules as create,
// except that type is immutable: it is still required and must equal the
// stored type (422 on "type" otherwise). uid in the body is ignored; the
// existing uid is kept. Requires the client's version (If-Match or
// "version"): 428 if missing, 409 if stale. The amounts sent override what
// transactions did to the balance (manual override).
func (s *Server) replaceBalance(c *gin.Context) {
	cur, ok := s.resolveBalance(c)
	if !ok {
		return
	}
	var in balance.Input
	if !decodeBody(c, &in, true) {
		return
	}
	version, ok := clientVersion(c, in.Version, true)
	if !ok {
		return
	}
	s.saveBalance(c, cur, store.WriteOptions{Version: version}, func(existing balance.Balance) (balance.Balance, error) {
		return in.ValidateUpdate(existing.Type)
	})
}

// deleteBalance handles DELETE. An If-Match header is optional; if sent and
// stale the answer is 409 with the current balance.
func (s *Server) deleteBalance(c *gin.Context) {
	cur, ok := s.resolveBalance(c)
	if !ok {
		return
	}
	version, ok := clientVersion(c, nil, false)
	if !ok {
		return
	}
	err := s.Store.DeleteBalanceWith(c.Request.Context(), cur.ID, store.WriteOptions{Version: version})
	if errors.Is(err, store.ErrVersionConflict) {
		s.balanceConflict(c, cur.UID)
		return
	}
	if err != nil {
		writeBalanceError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
