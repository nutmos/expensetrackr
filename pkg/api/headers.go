package api

import "github.com/gin-gonic/gin"

// ContentSecurityPolicy is the CSP sent with every response. The web app
// (pkg/web/static) loads only same-origin scripts and styles, has no inline
// <script>/<style> or style="" attributes and renders user data with
// textContent, so 'self' is enough. Swagger UI (/swagger/) replaces it with
// apidoc.UIContentSecurityPolicy.
const ContentSecurityPolicy = "default-src 'self'; base-uri 'none'; object-src 'none'; form-action 'self'; frame-ancestors 'none'"

// securityHeaders sets the browser hardening headers on every response
// (issue #82): no MIME sniffing, no framing (clickjacking), a same-origin
// Referer only and a strict CSP as a second layer against XSS.
func securityHeaders(c *gin.Context) {
	h := c.Writer.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("Content-Security-Policy", ContentSecurityPolicy)
	c.Next()
}
