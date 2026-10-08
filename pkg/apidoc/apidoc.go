// Package apidoc embeds the hand-written OpenAPI 3.0 description of the JSON
// API (openapi.yaml, the single source of truth) and an offline copy of
// Swagger UI, and serves them:
//
//	GET /api/openapi.yaml   the spec as written
//	GET /api/openapi.json   the same spec converted to JSON
//	GET /swagger/           Swagger UI (assets under /swagger/*; /swagger redirects)
//
// The Swagger UI files in swagger-ui/ are vendored from swagger-ui-dist (see
// swagger-ui/VENDOR.md); nothing is loaded from a CDN.
package apidoc

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/goccy/go-yaml"
)

// SwaggerUIVersion is the vendored swagger-ui-dist version.
const SwaggerUIVersion = "5.33.1"

// UIPath is where Swagger UI is mounted.
const UIPath = "/swagger"

//go:embed openapi.yaml
var specYAML []byte

//go:embed swagger-ui
var uiEmbed embed.FS

// uiFiles are the files served under /swagger/ (documentation files such as
// VENDOR.md are embedded but not served).
var uiFiles = map[string]bool{
	"index.html":             true,
	"swagger-initializer.js": true,
	"swagger-ui-bundle.js":   true,
	"swagger-ui.css":         true,
	"favicon-16x16.png":      true,
	"favicon-32x32.png":      true,
	"LICENSE":                true,
	"NOTICE":                 true,
}

// YAML returns the embedded OpenAPI document as written.
func YAML() []byte { return specYAML }

var jsonOnce = sync.OnceValues(func() ([]byte, error) { return yaml.YAMLToJSON(specYAML) })

// JSON returns the OpenAPI document converted to JSON (computed once).
func JSON() ([]byte, error) { return jsonOnce() }

// UI returns the Swagger UI files (index.html, the vendored bundle, ...).
func UI() fs.FS {
	sub, err := fs.Sub(uiEmbed, "swagger-ui")
	if err != nil {
		panic(err)
	}
	return sub
}

// ServeYAML handles GET /api/openapi.yaml.
func ServeYAML(c *gin.Context) {
	c.Header("Cache-Control", "no-cache")
	c.Data(http.StatusOK, "application/yaml; charset=utf-8", specYAML)
}

// ServeJSON handles GET /api/openapi.json.
func ServeJSON(c *gin.Context) {
	b, err := JSON()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	c.Header("Cache-Control", "no-cache")
	c.Data(http.StatusOK, "application/json; charset=utf-8", b)
}

// RegisterUI mounts Swagger UI at /swagger/ (GET /swagger redirects there).
// Unknown files answer the API's JSON 404.
func RegisterUI(r gin.IRoutes) {
	ui := UI()
	files := http.FileServer(http.FS(ui))
	r.GET(UIPath, func(c *gin.Context) {
		c.Redirect(http.StatusMovedPermanently, UIPath+"/")
	})
	r.GET(UIPath+"/*filepath", func(c *gin.Context) {
		name := strings.TrimPrefix(path.Clean(c.Param("filepath")), "/")
		if name == "" || name == "." {
			name = "index.html"
		}
		if !uiFiles[name] {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		if name == "index.html" {
			// Served directly: http.FileServer would redirect /index.html to ./
			b, err := fs.ReadFile(ui, name)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
				return
			}
			c.Header("Cache-Control", "no-cache")
			c.Data(http.StatusOK, "text/html; charset=utf-8", b)
			return
		}
		req := c.Request.Clone(c.Request.Context())
		req.URL.Path = "/" + name
		files.ServeHTTP(c.Writer, req)
	})
}
