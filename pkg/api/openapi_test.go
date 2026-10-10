package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/nutmos/expensetrackr/pkg/apidoc"
	"github.com/nutmos/expensetrackr/pkg/balance"
	"github.com/nutmos/expensetrackr/pkg/category"
	"github.com/nutmos/expensetrackr/pkg/transaction"
	"github.com/nutmos/expensetrackr/pkg/user"

	"github.com/gin-gonic/gin"
	"github.com/goccy/go-yaml"
)

// TestOpenAPIRoutes keeps the spec and the router in lockstep: every
// registered /api route (method + path, Gin :param mapped to {param}) must be
// an operation in the spec, and every operation in the spec must be a route.
// It also checks that the spec parses as an OpenAPI 3.0 document.
func TestOpenAPIRoutes(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	r := (&Server{}).Router()

	var routes []string
	for _, rt := range r.Routes() {
		if !strings.HasPrefix(rt.Path, "/api/") {
			continue
		}
		routes = append(routes, rt.Method+" "+ginToOpenAPIPath(rt.Path))
	}
	sort.Strings(routes)
	if len(routes) == 0 {
		t.Fatal("no /api routes registered")
	}

	var doc struct {
		OpenAPI string                    `yaml:"openapi"`
		Paths   map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(apidoc.YAML(), &doc); err != nil {
		t.Fatalf("openapi.yaml is not valid YAML: %v", err)
	}
	if !strings.HasPrefix(doc.OpenAPI, "3.0.") {
		t.Errorf("openapi version %q, want 3.0.x", doc.OpenAPI)
	}
	var ops []string
	for p, item := range doc.Paths {
		if !strings.HasPrefix(p, "/api/") {
			t.Errorf("spec path %s is outside /api (the spec documents the JSON API only)", p)
		}
		for method := range item {
			switch strings.ToLower(method) {
			case "get", "put", "post", "patch", "delete":
				ops = append(ops, strings.ToUpper(method)+" "+p)
			case "parameters", "servers", "summary", "description":
			default:
				t.Errorf("spec has unsupported method %s on %s", method, p)
			}
		}
	}
	sort.Strings(ops)

	missing, extra := diff(routes, ops)
	if len(missing) > 0 {
		t.Errorf("routes missing from docs/openapi.yaml (pkg/apidoc/openapi.yaml):\n  %s", strings.Join(missing, "\n  "))
	}
	if len(extra) > 0 {
		t.Errorf("spec operations with no route:\n  %s", strings.Join(extra, "\n  "))
	}
	if t.Failed() {
		t.Logf("routes (%d): %s", len(routes), strings.Join(routes, ", "))
	}
}

func ginToOpenAPIPath(p string) string {
	return regexp.MustCompile(`:([A-Za-z0-9_]+)`).ReplaceAllString(p, "{$1}")
}

func diff(have, want []string) (missing, extra []string) {
	i, j := 0, 0
	for i < len(have) && j < len(want) {
		switch {
		case have[i] == want[j]:
			i++
			j++
		case have[i] < want[j]:
			missing = append(missing, have[i])
			i++
		default:
			extra = append(extra, want[j])
			j++
		}
	}
	return append(missing, have[i:]...), append(extra, want[j:]...)
}

// TestOpenAPIJSON makes sure the served JSON form parses and carries the same
// operations as the YAML.
func TestOpenAPIJSON(t *testing.T) {
	b, err := apidoc.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("openapi json: %v", err)
	}
	if _, ok := doc["paths"].(map[string]any); !ok {
		t.Fatal("json spec has no paths")
	}
	if !bytes.Contains(b, []byte(`"/api/transactions"`)) {
		t.Error("json spec missing /api/transactions")
	}
}

// TestOpenAPIResponseShapes compares the JSON field set of each response
// schema in the spec with the struct the handler actually encodes, so the
// docs can't silently drift from the code. Optional fields are still always
// present in responses (nil pointers encode as null).
func TestOpenAPIResponseShapes(t *testing.T) {
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Required   []string       `yaml:"required"`
				Properties map[string]any `yaml:"properties"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(apidoc.YAML(), &doc); err != nil {
		t.Fatal(err)
	}
	check := func(schema string, v any) {
		t.Helper()
		sc, ok := doc.Components.Schemas[schema]
		if !ok {
			t.Errorf("spec has no schema %s", schema)
			return
		}
		got := jsonFields(v)
		want := map[string]bool{}
		for k := range sc.Properties {
			want[k] = true
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("schema %s fields differ from %T JSON:\n  code only: %v\n  spec only: %v",
				schema, v, only(got, want), only(want, got))
		}
		req := map[string]bool{}
		for _, k := range sc.Required {
			req[k] = true
		}
		if !reflect.DeepEqual(req, want) {
			t.Errorf("schema %s required list is not all of its properties (responses always include every field): %v",
				schema, only(want, req))
		}
	}
	check("Transaction", transaction.Transaction{})
	check("Balance", balance.Balance{})
	check("BalanceTotals", balance.Totals{})
	check("Category", category.Category{})
	check("User", user.User{})
	check("Identity", user.Identity{})
}

func jsonFields(v any) map[string]bool {
	out := map[string]bool{}
	t := reflect.TypeOf(v)
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		out[strings.Split(tag, ",")[0]] = true
	}
	return out
}

func only(a, b map[string]bool) []string {
	var out []string
	for k := range a {
		if !b[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// TestOpenAPIServed checks the routes that publish the spec and Swagger UI.
func TestOpenAPIServed(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	srv := httptest.NewServer((&Server{}).Router())
	defer srv.Close()

	get := func(path string) *http.Response {
		t.Helper()
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { res.Body.Close() })
		return res
	}
	body := func(res *http.Response) []byte {
		t.Helper()
		b, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	res := get("/api/openapi.yaml")
	if res.StatusCode != http.StatusOK || !strings.Contains(res.Header.Get("Content-Type"), "yaml") {
		t.Fatalf("yaml: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	if !bytes.Equal(body(res), apidoc.YAML()) {
		t.Error("/api/openapi.yaml is not the embedded spec")
	}

	res = get("/api/openapi.json")
	if res.StatusCode != http.StatusOK || !strings.Contains(res.Header.Get("Content-Type"), "json") {
		t.Fatalf("json: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	var doc map[string]any
	if err := json.Unmarshal(body(res), &doc); err != nil {
		t.Fatalf("served json: %v", err)
	}

	res = get("/swagger")
	if res.StatusCode != http.StatusOK || !strings.Contains(res.Request.URL.Path, "/swagger/") {
		t.Fatalf("/swagger did not redirect to /swagger/: %d %s", res.StatusCode, res.Request.URL)
	}

	res = get("/swagger/")
	html := string(body(res))
	if res.StatusCode != http.StatusOK || !strings.Contains(res.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("ui: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	for _, ref := range []string{"swagger-ui.css", "swagger-ui-bundle.js", "swagger-initializer.js", "/api/openapi.yaml"} {
		if !strings.Contains(html, ref) {
			t.Errorf("swagger page does not reference %s", ref)
		}
	}
	// Offline: no CDN and no online validator.
	if strings.Contains(html, "://") && strings.Contains(strings.ToLower(html), "cdn") {
		t.Error("swagger page references a CDN")
	}

	res = get("/swagger/swagger-ui-bundle.js")
	if res.StatusCode != http.StatusOK || !strings.Contains(res.Header.Get("Content-Type"), "javascript") {
		t.Fatalf("bundle: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	if !bytes.Contains(body(res), []byte("SwaggerUIBundle")) {
		t.Error("bundle does not define SwaggerUIBundle")
	}

	res = get("/swagger/nope.js")
	if res.StatusCode != http.StatusNotFound || !bytes.Contains(body(res), []byte(`"error":"not found"`)) {
		t.Error("unknown swagger asset should be the JSON 404")
	}
}
