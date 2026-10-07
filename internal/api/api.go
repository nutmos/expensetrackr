// Package api wires the HTTP routes (JSON API + web page) using Gin.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"expense-service/internal/expense"
	"expense-service/internal/store"
	"expense-service/internal/validate"

	"github.com/gin-gonic/gin"
)

const (
	maxBodyBytes = 64 << 10
	defaultLimit = 500
	maxLimit     = 5000
)

// Server holds dependencies for the HTTP handlers.
type Server struct {
	Store *store.Store
	// Static is the web assets filesystem; it must contain index.html.
	Static fs.FS
}

// errorBody is the JSON shape of every error response.
type errorBody struct {
	Error  string            `json:"error"`
	Fields map[string]string `json:"fields,omitempty"`
}

// Router builds the Gin engine with all routes.
func (s *Server) Router() *gin.Engine {
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())
	_ = r.SetTrustedProxies(nil)

	api := r.Group("/api")
	api.POST("/expenses", s.createExpense)
	api.GET("/expenses", s.listExpenses)
	api.GET("/expenses/:id", s.getExpense)
	api.PUT("/expenses/:id", s.replaceExpense)
	api.PATCH("/expenses/:id", s.patchExpense)
	api.DELETE("/expenses/:id", s.deleteExpense)
	s.registerBalanceRoutes(api)
	api.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })

	if s.Static != nil {
		r.GET("/", s.index)
		r.StaticFS("/static", http.FS(s.Static))
	}
	r.NoRoute(func(c *gin.Context) {
		c.JSON(http.StatusNotFound, errorBody{Error: "not found"})
	})
	return r
}

func (s *Server) index(c *gin.Context) {
	b, err := fs.ReadFile(s.Static, "index.html")
	if err != nil {
		c.String(http.StatusInternalServerError, "index.html missing")
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", b)
}

// decodeBody decodes a single JSON value from the request body into dst,
// writing a 400 response and returning false on failure.
func decodeBody(c *gin.Context, dst any, disallowUnknown bool) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBodyBytes)
	dec := json.NewDecoder(c.Request.Body)
	if disallowUnknown {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(dst); err != nil {
		c.JSON(http.StatusBadRequest, errorBody{Error: describeJSONError(err)})
		return false
	}
	if dec.More() {
		c.JSON(http.StatusBadRequest, errorBody{Error: "request body must contain a single JSON object"})
		return false
	}
	return true
}

// writeInputError maps validation/request errors to 422/400, anything else to 500.
func writeInputError(c *gin.Context, err error) {
	var ve *validate.ValidationError
	var re *validate.RequestError
	switch {
	case errors.As(err, &ve):
		c.JSON(http.StatusUnprocessableEntity, errorBody{Error: "validation failed", Fields: ve.Fields})
	case errors.As(err, &re):
		c.JSON(http.StatusBadRequest, errorBody{Error: re.Msg})
	case errors.Is(err, store.ErrNotFound):
		c.JSON(http.StatusNotFound, errorBody{Error: "expense not found"})
	default:
		internalError(c, err)
	}
}

func (s *Server) createExpense(c *gin.Context) {
	var in expense.CreateInput
	if !decodeBody(c, &in, true) {
		return
	}
	e, err := in.Validate()
	if err != nil {
		writeInputError(c, err)
		return
	}
	if err := s.Store.Create(c.Request.Context(), &e); err != nil {
		internalError(c, err)
		return
	}
	c.Header("Location", fmt.Sprintf("/api/expenses/%d", e.ID))
	c.JSON(http.StatusCreated, e)
}

// replaceExpense handles PUT: a full replace with the same rules as create.
// Omitted fields are treated as empty (so an omitted note clears it).
func (s *Server) replaceExpense(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var in expense.CreateInput
	if !decodeBody(c, &in, true) {
		return
	}
	next, err := in.Validate()
	if err != nil {
		// Validate before touching the DB, but report 404 first if the
		// expense does not exist, to match the PATCH behaviour.
		if _, gerr := s.Store.Get(c.Request.Context(), id); errors.Is(gerr, store.ErrNotFound) {
			writeInputError(c, gerr)
			return
		}
		writeInputError(c, err)
		return
	}
	updated, err := s.Store.Update(c.Request.Context(), id, func(expense.Expense) (expense.Expense, error) {
		return next, nil
	})
	if err != nil {
		writeInputError(c, err)
		return
	}
	c.JSON(http.StatusOK, updated)
}

// patchExpense handles PATCH: only fields present in the body change, then the
// merged result is validated as a whole (e.g. the amount is re-parsed against
// the new currency's minor-unit scale when the currency changes).
func (s *Server) patchExpense(c *gin.Context) {
	id, ok := parseID(c)
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
	updated, err := s.Store.Update(c.Request.Context(), id, func(cur expense.Expense) (expense.Expense, error) {
		in := cur.Input()
		if err := in.ApplyPatch(patch); err != nil {
			return expense.Expense{}, err
		}
		next, err := in.Validate()
		var ve *validate.ValidationError
		_, hasCur := patch["currency"]
		_, hasAmt := patch["amount"]
		if hasCur && !hasAmt && errors.As(err, &ve) && ve.Fields["amount"] != "" {
			ve.Fields["amount"] = fmt.Sprintf("the current amount %s (%s) cannot be expressed in %s: %s; send a new amount together with the currency (no exchange-rate conversion is done)",
				cur.Amount, cur.Currency, in.Currency, ve.Fields["amount"])
		}
		return next, err
	})
	if err != nil {
		writeInputError(c, err)
		return
	}
	c.JSON(http.StatusOK, updated)
}

func (s *Server) listExpenses(c *gin.Context) {
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
		t, err := expense.ParseTimestamp(raw)
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
	c.JSON(http.StatusOK, gin.H{"expenses": items, "count": len(items)})
}

func (s *Server) getExpense(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	e, err := s.Store.Get(c.Request.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		c.JSON(http.StatusNotFound, errorBody{Error: "expense not found"})
		return
	}
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, e)
}

func (s *Server) deleteExpense(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	err := s.Store.Delete(c.Request.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		c.JSON(http.StatusNotFound, errorBody{Error: "expense not found"})
		return
	}
	if err != nil {
		internalError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func parseID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id < 1 {
		c.JSON(http.StatusBadRequest, errorBody{Error: "id must be a positive integer"})
		return 0, false
	}
	return id, true
}

func internalError(c *gin.Context, err error) {
	log.Printf("internal error: %v", err)
	c.JSON(http.StatusInternalServerError, errorBody{Error: "internal server error"})
}

func describeJSONError(err error) string {
	var syn *json.SyntaxError
	var typ *json.UnmarshalTypeError
	var mbe *http.MaxBytesError
	switch {
	case errors.Is(err, io.EOF):
		return "request body is empty; expected a JSON object"
	case errors.As(err, &syn):
		return fmt.Sprintf("malformed JSON at byte %d", syn.Offset)
	case errors.As(err, &typ) && typ.Field == "":
		return "request body must be a JSON object"
	case errors.As(err, &typ):
		return fmt.Sprintf("field %q must be a %s", typ.Field, typ.Type)
	case errors.As(err, &mbe):
		return fmt.Sprintf("request body too large (max %d bytes)", mbe.Limit)
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		return "unknown field " + strings.TrimPrefix(err.Error(), "json: unknown field ")
	default:
		return "invalid JSON: " + err.Error()
	}
}
