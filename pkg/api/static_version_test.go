package api

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/nutmos/expensetrackr/pkg/web"
)

func TestVersionedIndex(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html": {Data: []byte(`<link href="/static/a.css"><script src="/static/a.js"></script><script src="/static/gone.js"></script>`)},
		"a.css":      {Data: []byte("body{}")},
		"a.js":       {Data: []byte("v1")},
	}
	b, err := versionedIndex(fsys)
	if err != nil {
		t.Fatal(err)
	}
	html := string(b)
	if !regexp.MustCompile(`href="/static/a\.css\?v=[0-9a-f]{12}"`).MatchString(html) ||
		!regexp.MustCompile(`src="/static/a\.js\?v=[0-9a-f]{12}"`).MatchString(html) {
		t.Errorf("assets not versioned: %s", html)
	}
	if !strings.Contains(html, `src="/static/gone.js"`) {
		t.Errorf("missing file reference changed: %s", html)
	}

	// Changing a file changes its URL.
	fsys["a.js"] = &fstest.MapFile{Data: []byte("v2")}
	b2, _ := versionedIndex(fsys)
	if string(b2) == html {
		t.Error("URL did not change when the file changed")
	}
}

// The served page references every real asset with a version, and the
// versioned URL itself is served.
func TestIndexServesVersionedAssets(t *testing.T) {
	r := (&Server{Static: web.Static()}).Router()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/transactions", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control = %q", cc)
	}
	refs := regexp.MustCompile(`(?:src|href)="(/static/[^"]+)"`).FindAllStringSubmatch(w.Body.String(), -1)
	if len(refs) == 0 {
		t.Fatal("no static references")
	}
	for _, m := range refs {
		if !strings.Contains(m[1], "?v=") {
			t.Errorf("unversioned asset %s", m[1])
			continue
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, m[1], nil))
		if w.Code != http.StatusOK {
			t.Errorf("GET %s: %d", m[1], w.Code)
		}
	}
}
