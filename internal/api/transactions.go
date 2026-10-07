package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"expense-service/internal/store"
	"expense-service/internal/transaction"
	"expense-service/internal/validate"

	"github.com/gin-gonic/gin"
)

const (
	defaultLimit = 500
	maxLimit     = 5000
)

// registerTransactionRoutes mounts /api/transactions under the given group.
func (s *Server) registerTransactionRoutes(api *gin.RouterGroup) {
	api.POST("/transactions", s.createTransaction)
	api.GET("/transactions", s.listTransactions)
	api.GET("/transactions/:uid", s.getTransaction)
	api.PUT("/transactions/:uid", s.replaceTransaction)
	api.PATCH("/transactions/:uid", s.patchTransaction)
	api.DELETE("/transactions/:uid", s.deleteTransaction)
}

// attachPaymentBalance loads the balance for e.BalanceUID, checks its type is
// allowed for e.Type, and sets e.Account to the balance's current name. For a
// transfer it also resolves to_balance_uid and sets ToAccount. Returns a *validate.ValidationError on balance_uid
// when the balance is missing or not payable.
func (s *Server) attachPaymentBalance(c *gin.Context, e *transaction.Transaction) error {
	b, err := s.Store.GetBalanceByUID(c.Request.Context(), e.BalanceUID)
	if errors.Is(err, store.ErrNotFound) {
		return &validate.ValidationError{Fields: map[string]string{
			"balance_uid": "does not match any balance",
		}}
	}
	if err != nil {
		return err
	}
	if err := e.AttachPaymentBalance(b); err != nil {
		return err
	}
	if e.Type != transaction.Transfer || e.ToBalanceUID == nil {
		e.ToBalanceUID, e.ToAccount = nil, nil
		return nil
	}
	to, err := s.Store.GetBalanceByUID(c.Request.Context(), *e.ToBalanceUID)
	if errors.Is(err, store.ErrNotFound) {
		return &validate.ValidationError{Fields: map[string]string{
			"to_balance_uid": "does not match any balance",
		}}
	}
	if err != nil {
		return err
	}
	return e.AttachDestinationBalance(to)
}

func (s *Server) createTransaction(c *gin.Context) {
	var in transaction.CreateInput
	if !decodeBody(c, &in, true) {
		return
	}
	e, err := in.Validate()
	if err != nil {
		writeInputError(c, err)
		return
	}
	if err := s.attachPaymentBalance(c, &e); err != nil {
		writeInputError(c, err)
		return
	}
	if err := s.Store.Create(c.Request.Context(), &e); err != nil {
		internalError(c, err)
		return
	}
	c.Header("Location", "/api/transactions/"+e.UID)
	c.JSON(http.StatusCreated, e)
}

// replaceTransaction handles PUT: a full replace with the same rules as create.
// Omitted fields are treated as empty (so an omitted note clears it).
func (s *Server) replaceTransaction(c *gin.Context) {
	id, ok := s.parseID(c)
	if !ok {
		return
	}
	var in transaction.CreateInput
	if !decodeBody(c, &in, true) {
		return
	}
	next, err := in.Validate()
	if err != nil {
		// Validate before touching the DB, but report 404 first if the
		// transaction does not exist, to match the PATCH behaviour.
		if _, gerr := s.Store.Get(c.Request.Context(), id); errors.Is(gerr, store.ErrNotFound) {
			writeInputError(c, gerr)
			return
		}
		writeInputError(c, err)
		return
	}
	if err := s.attachPaymentBalance(c, &next); err != nil {
		if _, gerr := s.Store.Get(c.Request.Context(), id); errors.Is(gerr, store.ErrNotFound) {
			writeInputError(c, gerr)
			return
		}
		writeInputError(c, err)
		return
	}
	updated, err := s.Store.Update(c.Request.Context(), id, func(transaction.Transaction) (transaction.Transaction, error) {
		return next, nil
	})
	if err != nil {
		writeInputError(c, err)
		return
	}
	c.JSON(http.StatusOK, updated)
}

// patchTransaction handles PATCH: only fields present in the body change, then the
// merged result is validated as a whole (e.g. the amount is re-parsed against
// the new currency's minor-unit scale when the currency changes).
func (s *Server) patchTransaction(c *gin.Context) {
	id, ok := s.parseID(c)
	if !ok {
		return
	}
	var patch map[string]json.RawMessage
	if !decodeBody(c, &patch, false) {
		return
	}
	if patch == nil { // body was JSON null
		c.JSON(http.StatusBadRequest, errorBody{Error: "request body must be a JSON object"})
		return
	}
	// Resolve the patch outside the write transaction: attachPaymentBalance
	// needs its own DB read, and the store uses a single SQLite connection.
	cur, err := s.Store.Get(c.Request.Context(), id)
	if err != nil {
		writeInputError(c, err)
		return
	}
	in := cur.Input()
	if err := in.ApplyPatch(patch); err != nil {
		writeInputError(c, err)
		return
	}
	next, err := in.Validate()
	var ve *validate.ValidationError
	_, hasCur := patch["currency"]
	_, hasAmt := patch["amount"]
	if hasCur && !hasAmt && errors.As(err, &ve) && ve.Fields["amount"] != "" {
		ve.Fields["amount"] = fmt.Sprintf("the current amount %s (%s) cannot be expressed in %s: %s; send a new amount together with the currency (no exchange-rate conversion is done)",
			cur.Amount, cur.Currency, in.Currency, ve.Fields["amount"])
		writeInputError(c, err)
		return
	}
	if err != nil {
		writeInputError(c, err)
		return
	}
	if err := s.attachPaymentBalance(c, &next); err != nil {
		writeInputError(c, err)
		return
	}
	updated, err := s.Store.Update(c.Request.Context(), id, func(transaction.Transaction) (transaction.Transaction, error) {
		return next, nil
	})
	if err != nil {
		writeInputError(c, err)
		return
	}
	c.JSON(http.StatusOK, updated)
}

func (s *Server) listTransactions(c *gin.Context) {
	fields := map[string]string{}
	var f store.ListFilter

	parseTime := func(name string) *time.Time {
		raw := c.Query(name)
		if raw == "" {
			return nil
		}
		// An unencoded "+08:00" in a query string decodes to " 08:00"; RFC 3339
		// never contains spaces, so restore the plus sign.
		raw = strings.ReplaceAll(raw, " ", "+")
		t, err := transaction.ParseTimestamp(raw)
		if err != nil {
			fields[name] = err.Error()
			return nil
		}
		return &t
	}
	f.From = parseTime("from")
	f.To = parseTime("to")
	if f.From != nil && f.To != nil && f.From.After(*f.To) {
		fields["to"] = "must not be earlier than from"
	}

	if raw := c.Query("type"); raw != "" {
		if t, ok := transaction.ParseType(raw); ok {
			f.Type = t
		} else {
			fields["type"] = "must be one of: expense, income, transfer"
		}
	}

	f.Limit = defaultLimit
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxLimit {
			fields["limit"] = fmt.Sprintf("must be an integer between 1 and %d", maxLimit)
		} else {
			f.Limit = n
		}
	}
	if len(fields) > 0 {
		c.JSON(http.StatusBadRequest, errorBody{Error: "invalid query parameters", Fields: fields})
		return
	}

	items, err := s.Store.List(c.Request.Context(), f)
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"transactions": items, "count": len(items)})
}

func (s *Server) getTransaction(c *gin.Context) {
	id, ok := s.parseID(c)
	if !ok {
		return
	}
	e, err := s.Store.Get(c.Request.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		c.JSON(http.StatusNotFound, errorBody{Error: "transaction not found"})
		return
	}
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, e)
}

func (s *Server) deleteTransaction(c *gin.Context) {
	id, ok := s.parseID(c)
	if !ok {
		return
	}
	err := s.Store.Delete(c.Request.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		c.JSON(http.StatusNotFound, errorBody{Error: "transaction not found"})
		return
	}
	if err != nil {
		internalError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// parseID resolves /api/transactions/:uid to the internal row id. Only the
// UUID uid is accepted: a malformed value is 400, an unknown uid is 404.
func (s *Server) parseID(c *gin.Context) (int64, bool) {
	uid, ok := pathUID(c)
	if !ok {
		return 0, false
	}
	e, err := s.Store.GetByUID(c.Request.Context(), uid)
	if errors.Is(err, store.ErrNotFound) {
		c.JSON(http.StatusNotFound, errorBody{Error: "transaction not found"})
		return 0, false
	}
	if err != nil {
		internalError(c, err)
		return 0, false
	}
	return e.ID, true
}
