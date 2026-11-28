package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/nutmos/expensetrackr/pkg/balance"
	"github.com/nutmos/expensetrackr/pkg/common_type"
	"github.com/nutmos/expensetrackr/pkg/helper"
	"github.com/nutmos/expensetrackr/pkg/store"

	"github.com/gin-gonic/gin"
)

func (s *Server) registerBalanceRoutes(api *gin.RouterGroup) {
	api.POST("/balances", s.createBalance)
	api.GET("/balances", s.listBalances)
	api.GET("/balances/:uid", s.getBalance)
	api.PUT("/balances/:uid", s.replaceBalance)
	api.DELETE("/balances/:uid", s.deleteBalance)
	api.POST("/balances/:uid/hide", s.hideBalance)
	api.POST("/balances/:uid/unhide", s.unhideBalance)
}

// queryFlag reports whether query parameter name is "1" or "true"
// (case-insensitive); any other value counts as not set.
func queryFlag(c *gin.Context, name string) bool {
	v := c.Query(name)
	return strings.EqualFold(v, "1") || strings.EqualFold(v, "true")
}

// resolveBalance looks up /api/balances/:uid. Only the UUID uid is accepted:
// a malformed value is 400, an unknown uid is 404.
func (s *Server) resolveBalance(c *gin.Context) (balance.Balance, bool) {
	uid, ok := pathUID(c)
	if !ok {
		return balance.Balance{}, false
	}
	b, err := s.Store.GetBalanceByUID(c.Request.Context(), sessionUserUID(c), uid)
	if err != nil {
		writeBalanceError(c, err)
		return balance.Balance{}, false
	}
	return b, true
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
	if err := s.Store.CreateBalance(c.Request.Context(), sessionUserUID(c), &b); err != nil {
		writeBalanceError(c, err)
		return
	}
	c.Header("Location", "/api/balances/"+string(b.UID))
	setETag(c, b.Version)
	c.JSON(http.StatusCreated, b)
}

func (s *Server) listBalances(c *gin.Context) {
	typ := balance.Type(helper.Normalize(c.Query("type")))
	payableOnly := queryFlag(c, "payable")
	includeHidden := queryFlag(c, "include_hidden")
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
		items, err = s.Store.ListPayableBalances(c.Request.Context(), sessionUserUID(c))
	} else {
		items, err = s.Store.ListBalances(c.Request.Context(), sessionUserUID(c), typ)
	}
	if err != nil {
		internalError(c, err)
		return
	}
	if !includeHidden {
		shown := items[:0]
		for _, b := range items {
			if !b.Hidden {
				shown = append(shown, b)
			}
		}
		items = shown
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
func (s *Server) balanceConflict(c *gin.Context, uid common_type.UID) {
	cur, err := s.Store.GetBalanceByUID(c.Request.Context(), sessionUserUID(c), uid)
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
	updated, err := s.Store.UpdateBalanceWith(c.Request.Context(), sessionUserUID(c), cur.ID, opts, fn)
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

// deleteBalance handles DELETE: the balance is removed together with every
// transaction that references it, and those transactions' effects on the
// user's other balances are reversed (see store.DeleteBalanceWith). To keep
// the transactions, hide the balance instead (POST .../hide). An If-Match
// header is optional; if sent and stale the answer is 409 with the current
// balance. 200 with the number of transactions removed.
func (s *Server) deleteBalance(c *gin.Context) {
	cur, ok := s.resolveBalance(c)
	if !ok {
		return
	}
	version, ok := clientVersion(c, nil, false)
	if !ok {
		return
	}
	n, err := s.Store.DeleteBalanceWith(c.Request.Context(), sessionUserUID(c), cur.ID, store.WriteOptions{Version: version})
	if errors.Is(err, store.ErrVersionConflict) {
		s.balanceConflict(c, cur.UID)
		return
	}
	if err != nil {
		writeBalanceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted_transactions": n})
}

// hideBalance handles POST /api/balances/:uid/hide: the balance is left out
// of the list (unless ?include_hidden=true) but kept, with its transactions.
func (s *Server) hideBalance(c *gin.Context) { s.setBalanceHidden(c, true) }

// unhideBalance handles POST /api/balances/:uid/unhide: the reverse of hide.
func (s *Server) unhideBalance(c *gin.Context) { s.setBalanceHidden(c, false) }

// setBalanceHidden takes no body. If-Match is optional (409 if stale). 200
// with the balance; its version goes up only if the flag changed.
func (s *Server) setBalanceHidden(c *gin.Context, hidden bool) {
	cur, ok := s.resolveBalance(c)
	if !ok {
		return
	}
	version, ok := clientVersion(c, nil, false)
	if !ok {
		return
	}
	b, err := s.Store.SetBalanceHidden(c.Request.Context(), sessionUserUID(c), cur.ID, store.WriteOptions{Version: version}, hidden)
	if errors.Is(err, store.ErrVersionConflict) {
		s.balanceConflict(c, cur.UID)
		return
	}
	if err != nil {
		writeBalanceError(c, err)
		return
	}
	setETag(c, b.Version)
	c.JSON(http.StatusOK, b)
}
