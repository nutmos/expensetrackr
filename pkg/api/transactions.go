package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nutmos/expensetrackr/pkg/balance"
	"github.com/nutmos/expensetrackr/pkg/common_type"
	"github.com/nutmos/expensetrackr/pkg/store"
	"github.com/nutmos/expensetrackr/pkg/transaction"
	"github.com/nutmos/expensetrackr/pkg/validate"

	"github.com/gin-gonic/gin"
)

const (
	defaultLimit = 500
	maxLimit     = 5000
)

// attachCategory resolves e.CategoryUID (if any) among the logged-in user's
// categories and checks it matches the transaction type. Only the uid is stored.
func (s *Server) attachCategory(c *gin.Context, e *transaction.Transaction) error {
	if e.CategoryUID == nil {
		return nil
	}
	cat, err := s.Store.GetCategoryByUID(c.Request.Context(), sessionUserUID(c), *e.CategoryUID)
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
	api.POST("/transactions/bulk", s.bulkCreateTransactions)
	api.GET("/transactions", s.listTransactions)
	api.GET("/transactions/:uid", s.getTransaction)
	api.PUT("/transactions/:uid", s.replaceTransaction)
	api.DELETE("/transactions/:uid", s.deleteTransaction)
}

// attachPaymentBalance resolves everything a validated transaction refers to,
// among the logged-in user's records:
//
//   - balance_uid: must exist and be allowed for e.Type; sets e.Account;
//   - category_uid (if any): must exist and match the type (attachCategory);
//   - to_balance_uid (transfer, asset purchase, return on investment): must
//     exist and be allowed as destination; sets e.ToAccount;
//   - for a transfer, the cross-currency fields (convertTransfer), which may
//     fetch an exchange rate. prev is the stored transaction on PUT.
//
// A missing or unsuitable reference is a *validate.ValidationError on that
// field.
func (s *Server) attachPaymentBalance(c *gin.Context, e *transaction.Transaction, in transaction.CreateInput, prev *transaction.Transaction) error {
	b, err := s.Store.GetBalanceByUID(c.Request.Context(), sessionUserUID(c), e.BalanceUID)
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
	if !e.Type.HasDestination() || e.ToBalanceUID == nil {
		e.ToBalanceUID, e.ToAccount = nil, nil
		return s.convertTransfer(c, e, in, b, balance.Balance{}, prev)
	}
	to, err := s.Store.GetBalanceByUID(c.Request.Context(), sessionUserUID(c), *e.ToBalanceUID)
	if errors.Is(err, store.ErrNotFound) {
		return &validate.ValidationError{Fields: map[string]string{
			"to_balance_uid": "does not match any balance",
		}}
	}
	if err != nil {
		return err
	}
	if err := e.AttachDestinationBalance(to); err != nil {
		return err
	}
	return s.convertTransfer(c, e, in, b, to, prev)
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
	if err := s.attachPaymentBalance(c, &e, in, nil); err != nil {
		writeInputError(c, err)
		return
	}
	// Create also moves the balances (same DB transaction); a missing balance
	// or a currency mismatch comes back as a validation error (422).
	if err := s.Store.Create(c.Request.Context(), sessionUserUID(c), &e); err != nil {
		writeInputError(c, err)
		return
	}
	c.Header("Location", "/api/transactions/"+string(e.UID))
	setETag(c, e.Version)
	c.JSON(http.StatusCreated, e)
}

// transactionConflict answers 409 with the transaction as it is now (or 404
// if it was deleted meanwhile).
func (s *Server) transactionConflict(c *gin.Context, id int64) {
	cur, err := s.Store.Get(c.Request.Context(), sessionUserUID(c), id)
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
	updated, err := s.Store.UpdateWith(c.Request.Context(), sessionUserUID(c), cur.ID, store.WriteOptions{Version: version}, func(transaction.Transaction) (transaction.Transaction, error) {
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
	if cur.Type == transaction.BalanceAdjustment { // read-only, whatever the body or version
		writeInputError(c, store.ErrReadOnly)
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
	if in.ImportRef == nil { // omitted: keep the stored ref ("" clears it)
		next.ImportRef = cur.ImportRef
	}
	if err := s.attachPaymentBalance(c, &next, in, &cur); err != nil {
		writeInputError(c, err)
		return
	}
	s.saveTransaction(c, cur, version, next)
}

func (s *Server) listTransactions(c *gin.Context) {
	f, fields := parseListFilter(c)
	if len(fields) > 0 {
		c.JSON(http.StatusBadRequest, errorBody{Error: "invalid query parameters", Fields: fields})
		return
	}
	items, next, err := s.Store.ListPage(c.Request.Context(), sessionUserUID(c), f)
	if err != nil {
		internalError(c, err)
		return
	}
	// next_cursor is null on the last page; otherwise pass it back as
	// ?cursor= (with the same filters) to get the next page.
	var nextCursor *string
	if next != nil {
		s := next.String()
		nextCursor = &s
	}
	c.JSON(http.StatusOK, gin.H{"transactions": items, "count": len(items), "next_cursor": nextCursor})
}

// parseListFilter reads the GET /api/transactions query (from, to, type,
// category_uid, balance_uid, limit, cursor) into a store.ListFilter. Invalid parameters are
// returned as field messages (the handler answers 400 with them).
func parseListFilter(c *gin.Context) (store.ListFilter, map[string]string) {
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
		if t, ok := transaction.ParseFilterType(raw); ok {
			f.Type = t
		} else {
			fields["type"] = "must be one of: expense, income, transfer, asset_purchase, return_on_investment, balance_adjustment"
		}
	}

	if raw := common_type.NormalizeUID(c.Query("category_uid")); raw != "" {
		if raw.Valid() {
			f.CategoryUID = raw
		} else {
			fields["category_uid"] = "must be a UUID"
		}
	}

	if raw := common_type.NormalizeUID(c.Query("balance_uid")); raw != "" {
		if raw.Valid() {
			f.BalanceUID = raw
		} else {
			fields["balance_uid"] = "must be a UUID"
		}
	}

	if raw := strings.TrimSpace(c.Query("cursor")); raw != "" {
		if cur, err := store.ParseListCursor(raw); err == nil {
			f.After = &cur
		} else {
			fields["cursor"] = "is not a cursor from this API (use next_cursor from the previous page)"
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
	return f, fields
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
	if cur.Type == transaction.BalanceAdjustment { // read-only, whatever the body or version
		writeInputError(c, store.ErrReadOnly)
		return
	}
	version, ok := clientVersion(c, nil, false)
	if !ok {
		return
	}
	err := s.Store.DeleteWith(c.Request.Context(), sessionUserUID(c), cur.ID, store.WriteOptions{Version: version})
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
	e, err := s.Store.GetByUID(c.Request.Context(), sessionUserUID(c), uid)
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
