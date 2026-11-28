package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

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
		writeTransactionError(c, err)
		return
	}
	if err := s.attachPaymentBalance(c, &e, in, nil); err != nil {
		writeTransactionError(c, err)
		return
	}
	// Create also moves the balances (same DB transaction); a missing balance
	// or a currency mismatch comes back as a validation error (422).
	if err := s.Store.Create(c.Request.Context(), sessionUserUID(c), &e); err != nil {
		writeTransactionError(c, err)
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
		writeTransactionError(c, err)
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
		writeTransactionError(c, err)
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
	validate := in.Validate
	switch cur.Type {
	case transaction.BalanceAdjustment:
		// Editable, but it stays a balance adjustment (see ValidateAdjustment).
		validate = in.ValidateAdjustment
	case transaction.BeginningBalance:
		// Editable, but it stays the beginning balance of its balance.
		validate = in.ValidateBeginningBalance
	}
	next, err := validate()
	if err != nil {
		writeTransactionError(c, err)
		return
	}
	if in.ImportRef == nil { // omitted: keep the stored ref ("" clears it)
		next.ImportRef = cur.ImportRef
	}
	if err := s.attachPaymentBalance(c, &next, in, &cur); err != nil {
		writeTransactionError(c, err)
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

// maxSearchLen is the longest ?q= accepted, in characters.
const maxSearchLen = 200

// parseListFilter reads the GET /api/transactions query (from, to, type,
// category_uid, balance_uid, q, limit, cursor) into a store.ListFilter. Invalid parameters are
// returned as field messages (the handler answers 400 with them).
func parseListFilter(c *gin.Context) (store.ListFilter, map[string]string) {
	fields := map[string]string{}
	var f store.ListFilter

	f.From, f.To = parseTimeRange(c, fields)

	if raw := c.Query("type"); raw != "" {
		if t, ok := transaction.ParseFilterType(raw); ok {
			f.Type = t
		} else {
			fields["type"] = "must be one of: expense, income, transfer, asset_purchase, return_on_investment, asset_sale, repay_debt, balance_adjustment, beginning_balance"
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

	if raw := strings.TrimSpace(c.Query("q")); raw != "" {
		if utf8.RuneCountInString(raw) > maxSearchLen {
			fields["q"] = fmt.Sprintf("must be at most %d characters", maxSearchLen)
		} else if len(store.SearchTerms(raw)) > store.MaxSearchTerms {
			fields["q"] = fmt.Sprintf("must have at most %d words", store.MaxSearchTerms)
		} else {
			f.Search = raw
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

// parseTimeRange reads the optional ?from= and ?to= query parameters
// (inclusive bounds). Each is either an RFC 3339 date-time with offset or a
// plain date (YYYY-MM-DD): a date means the whole day, so from is its first
// instant and to its last, in the time zone named by the optional ?tz=
// (IANA name such as Asia/Bangkok, or an offset such as +07:00; default
// UTC). Problems are added to fields.
func parseTimeRange(c *gin.Context, fields map[string]string) (from, to *time.Time) {
	loc := time.UTC
	if raw := queryTime(c, "tz"); strings.TrimSpace(raw) != "" {
		l, err := parseZone(raw)
		if err != nil {
			fields["tz"] = err.Error()
		} else {
			loc = l
		}
	}
	parseTime := func(name string, endOfDay bool) *time.Time {
		raw := strings.TrimSpace(queryTime(c, name))
		if raw == "" {
			return nil
		}
		if d, err := time.ParseInLocation(time.DateOnly, raw, loc); err == nil {
			if endOfDay {
				// The last instant of the day (a day is not always 24 hours).
				d = time.Date(d.Year(), d.Month(), d.Day()+1, 0, 0, 0, 0, loc).Add(-time.Nanosecond)
			}
			return &d
		}
		t, err := transaction.ParseTimestamp(raw)
		if err != nil {
			fields[name] = "must be a date (YYYY-MM-DD) or an RFC 3339 / ISO 8601 date-time with a timezone offset, e.g. 2026-10-06 or 2026-10-06T21:06:00+08:00"
			return nil
		}
		return &t
	}
	from, to = parseTime("from", false), parseTime("to", true)
	if from != nil && to != nil && from.After(*to) {
		fields["to"] = "must not be earlier than from"
	}
	return from, to
}

// parseZone reads a ?tz= value: "UTC", "Z", an offset ("+07:00", "-0530")
// or an IANA time zone name ("Asia/Bangkok").
func parseZone(raw string) (*time.Location, error) {
	raw = strings.TrimSpace(raw)
	if raw == "Z" || strings.EqualFold(raw, "UTC") {
		return time.UTC, nil
	}
	if raw[0] == '+' || raw[0] == '-' {
		for _, layout := range []string{"-07:00", "-0700", "-07"} {
			if t, err := time.Parse(layout, raw); err == nil {
				_, off := t.Zone()
				return time.FixedZone(raw, off), nil
			}
		}
	} else if loc, err := time.LoadLocation(raw); err == nil && raw != "Local" {
		return loc, nil
	}
	return nil, errors.New("must be an IANA time zone (e.g. Asia/Bangkok) or a UTC offset (e.g. +07:00)")
}

// queryTime is the raw ?name= timestamp. An unencoded "+08:00" in a query
// string decodes to " 08:00"; RFC 3339 never contains spaces, so the plus
// sign is restored.
func queryTime(c *gin.Context, name string) string {
	return strings.ReplaceAll(c.Query(name), " ", "+")
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
	err := s.Store.DeleteWith(c.Request.Context(), sessionUserUID(c), cur.ID, store.WriteOptions{Version: version})
	if errors.Is(err, store.ErrVersionConflict) {
		s.transactionConflict(c, cur.ID)
		return
	}
	if err != nil {
		writeTransactionError(c, err)
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
	if err != nil {
		writeTransactionError(c, err)
		return transaction.Transaction{}, false
	}
	return e, true
}
