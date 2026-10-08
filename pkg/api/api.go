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
	"strings"

	"github.com/nutmos/expensetrackr/pkg/apidoc"
	"github.com/nutmos/expensetrackr/pkg/store"
	"github.com/nutmos/expensetrackr/pkg/validate"

	"github.com/gin-gonic/gin"
)

const maxBodyBytes = 64 << 10

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
	// Code is a stable machine-readable reason for some errors
	// (version_required, version_conflict, balance_in_use).
	Code string `json:"code,omitempty"`
	// Current is the record as it is now, sent with a 409 version conflict.
	Current any `json:"current,omitempty"`
}

// Router builds the Gin engine with all routes.
func (s *Server) Router() *gin.Engine {
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())
	_ = r.SetTrustedProxies(nil)

	api := r.Group("/api")
	s.registerTransactionRoutes(api)
	s.registerBalanceRoutes(api)
	s.registerCategoryRoutes(api)
	s.registerUserRoutes(api)
	api.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	// API description (pkg/apidoc/openapi.yaml) and Swagger UI at /swagger/.
	api.GET("/openapi.yaml", apidoc.ServeYAML)
	api.GET("/openapi.json", apidoc.ServeJSON)
	apidoc.RegisterUI(r)

	if s.Static != nil {
		s.registerPageRoutes(r)
		r.StaticFS("/static", http.FS(s.Static))
	}
	r.NoRoute(func(c *gin.Context) {
		c.JSON(http.StatusNotFound, errorBody{Error: "not found"})
	})
	return r
}

// index serves the single-page web app (index.html) for every page URL.
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
		c.JSON(http.StatusNotFound, errorBody{Error: "transaction not found"})
	case errors.Is(err, store.ErrReadOnly):
		c.JSON(http.StatusConflict, errorBody{Error: err.Error(), Code: "balance_adjustment_readonly"})
	default:
		internalError(c, err)
	}
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
