package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/nutmos/expensetrackr/pkg/apidoc"

	"github.com/gin-gonic/gin"
)

// Every response carries the hardening headers (issue #82); Swagger UI gets
// its own, relaxed CSP. Only public routes and error responses are used, so
// no database is needed (Store is nil).
func TestSecurityHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	static := fstest.MapFS{
		"index.html": {Data: []byte("<!doctype html><title>Expense Log</title>")},
		"router.js":  {Data: []byte("// js")},
	}
	h := (&Server{Static: static}).Router()
	for _, tc := range []struct {
		method, path string
		code         int
		csp          string
	}{
		{"GET", "/", http.StatusOK, ContentSecurityPolicy},
		{"GET", "/transactions/new", http.StatusOK, ContentSecurityPolicy},
		{"GET", "/static/router.js", http.StatusOK, ContentSecurityPolicy},
		{"GET", "/api/healthz", http.StatusOK, ContentSecurityPolicy},
		{"GET", "/api/transactions", http.StatusUnauthorized, ContentSecurityPolicy},
		{"GET", "/api/openapi.json", http.StatusOK, ContentSecurityPolicy},
		{"GET", "/no-such-page", http.StatusNotFound, ContentSecurityPolicy},
		{"POST", "/api/transactions", http.StatusForbidden, ContentSecurityPolicy}, // cross-site, rejected by csrfGuard
		{"GET", "/swagger/", http.StatusOK, apidoc.UIContentSecurityPolicy},
		{"GET", "/swagger/swagger-ui.css", http.StatusOK, apidoc.UIContentSecurityPolicy},
		{"GET", "/swagger/missing.js", http.StatusNotFound, apidoc.UIContentSecurityPolicy},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		if tc.method == "POST" {
			req.Header.Set("Sec-Fetch-Site", "cross-site")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.code {
			t.Errorf("%s %s: status %d, want %d", tc.method, tc.path, rec.Code, tc.code)
		}
		for k, want := range map[string]string{
			"X-Content-Type-Options":  "nosniff",
			"X-Frame-Options":         "DENY",
			"Referrer-Policy":         "same-origin",
			"Content-Security-Policy": tc.csp,
		} {
			if got := rec.Header().Values(k); len(got) != 1 || got[0] != want {
				t.Errorf("%s %s: %s = %q, want %q", tc.method, tc.path, k, got, want)
			}
		}
	}
}
