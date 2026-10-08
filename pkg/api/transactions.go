package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nutmos/expensetrackr/pkg/store"
	"github.com/nutmos/expensetrackr/pkg/transaction"
	"github.com/nutmos/expensetrackr/pkg/validate"

	"github.com/gin-gonic/gin"
)

const (
	defaultLimit = 500
	maxLimit     = 5000
)

// attachCategory resolves e.CategoryUID (if any), checks it matches the
// transaction type. Only the uid is stored.
func (s *Server) attachCategory(c *gin.Context, e *transaction.Transaction) error {
	if e.CategoryUID == nil {
		return nil
	}
	cat, err := s.Store.GetCategoryByUID(c.Request.Context(), *e.CategoryUID)
	if errors.Is(err, store.ErrNotFound) {
		return &validate.ValidationError{Fields: map[string]string{"category_uid": "does not match any category"}}
	}
	if err != nil {
		return err
	}
	return e.AttachCategory(cat)
}

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
	if err := s.attachCategory(c, e); err != nil {
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
	// Create also moves the balances (same DB transaction); a missing balance
	// or a currency mismatch comes back as a validation error (422).
	if err := s.Store.Create(c.Request.Context(), &e); err != nil {
		writeInputError(c, err)
		return
	}
	c.Header("Location", "/api/transactions/"+e.UID)
	setETag(c, e.Version)
	c.JSON(http.StatusCreated, e)
}

// transactionConflict answers 409 with the transaction as it is now (or 404
// if it was deleted meanwhile).
func (s *Server) transactionConflict(c *gin.Context, id int64) {
	cur, err := s.Store.Get(c.Request.Context(), id)
	if err != nil {
		writeInputError(c, err)
		return
	}
	writeVersionConflict(c, "transaction", cur, cur.Version)
}

// saveTransaction stores next over cur with optimistic locking and writes the
// response: 200 + ETag, 409 on a stale version, 422 (e.g. currency mismatch
// with a balance) or 404.
func (s *Server) saveTransaction(c *gin.Context, cur transaction.Transaction, version int64, next transaction.Transaction) {
	updated, err := s.Store.UpdateWith(c.Request.Context(), cur.ID, store.WriteOptions{Version: version}, func(transaction.Transaction) (transaction.Transaction, error) {
		return next, nil
	})
	if errors.Is(err, store.ErrVersionConflict) {
		s.transactionConflict(c, cur.ID)
		return
	}
	if err != nil {
		writeInputError(c, err)
		return
	}
	setETag(c, updated.Version)
	c.JSON(http.StatusOK, updated)
}

// replaceTransaction handles PUT: a full replace with the same rules as create.
// Omitted fields are treated as empty (so an omitted note clears it).
// Requires the client's version (If-Match or "version"): 428 if missing, 409
// if stale (checked before validation, so a stale client reloads first).
func (s *Server) replaceTransaction(c *gin.Context) {
	cur, ok := s.resolveTransaction(c)
	if !ok {
		return
	}
	var in transaction.CreateInput
	if !decodeBody(c, &in, true) {
		return
	}
	version, ok := clientVersion(c, in.Version, true)
	if !ok {
		return
	}
	if version != cur.Version {
		s.transactionConflict(c, cur.ID)
		return
	}
	next, err := in.Validate()
	if err != nil {
		writeInputError(c, err)
		return
	}
	if err := s.attachPaymentBalance(c, &next); err != nil {
		writeInputError(c, err)
		return
	}
	s.saveTransaction(c, cur, version, next)
}

// patchTransaction handles PATCH: only fields present in the body change, then the
// merged result is validated as a whole (e.g. the amount is re-parsed against
// the new currency's minor-unit scale when the currency changes). Versioning
// as for PUT.
func (s *Server) patchTransaction(c *gin.Context) {
	cur, ok := s.resolveTransaction(c)
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
	bodyVersion, err := takeVersion(patch)
	if err != nil {
		writeInputError(c, err)
		return
	}
	version, ok := clientVersion(c, bodyVersion, true)
	if !ok {
		return
	}
	if version != cur.Version {
		s.transactionConflict(c, cur.ID)
		return
	}
	// Resolve the patch outside the write transaction: attachPaymentBalance
	// needs its own DB reads, and the store uses a single SQLite connection.
	// The version check inside UpdateWith catches any change in between.
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
	s.saveTransaction(c, cur, version, next)
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

	if raw := strings.ToLower(strings.TrimSpace(c.Query("category_uid"))); raw != "" {
		if uuidRE.MatchString(raw) {
			f.CategoryUID = raw
		} else {
			fields["category_uid"] = "must be a UUID"
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
	e, ok := s.resolveTransaction(c)
	if !ok {
		return
	}
	setETag(c, e.Version)
	c.JSON(http.StatusOK, e)
}

// deleteTransaction handles DELETE and reverses the transaction's balance
// effect. An If-Match header is optional; if sent and stale the answer is 409.
func (s *Server) deleteTransaction(c *gin.Context) {
	cur, ok := s.resolveTransaction(c)
	if !ok {
		return
	}
	version, ok := clientVersion(c, nil, false)
	if !ok {
		return
	}
	err := s.Store.DeleteWith(c.Request.Context(), cur.ID, store.WriteOptions{Version: version})
	if errors.Is(err, store.ErrVersionConflict) {
		s.transactionConflict(c, cur.ID)
		return
	}
	if err != nil {
		writeInputError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// resolveTransaction loads /api/transactions/:uid. Only the UUID uid is
// accepted: a malformed value is 400, an unknown uid is 404.
func (s *Server) resolveTransaction(c *gin.Context) (transaction.Transaction, bool) {
	uid, ok := pathUID(c)
	if !ok {
		return transaction.Transaction{}, false
	}
	e, err := s.Store.GetByUID(c.Request.Context(), uid)
	if errors.Is(err, store.ErrNotFound) {
		c.JSON(http.StatusNotFound, errorBody{Error: "transaction not found"})
		return transaction.Transaction{}, false
	}
	if err != nil {
		internalError(c, err)
		return transaction.Transaction{}, false
	}
	return e, true
}
