package web

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// The embedded page must reference only assets that exist, and contain one
// view per route the client router knows about.
func TestEmbeddedAssets(t *testing.T) {
	st := Static()
	index, err := fs.ReadFile(st, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(index)
	refs := regexp.MustCompile(`(?:src|href)="/static/([^"]+)"`).FindAllStringSubmatch(html, -1)
	if len(refs) < 5 {
		t.Fatalf("expected style.css + 4 scripts, got %v", refs)
	}
	for _, m := range refs {
		if _, err := fs.Stat(st, m[1]); err != nil {
			t.Errorf("index.html references missing asset %s", m[1])
		}
	}
	for _, id := range []string{
		"view-transactions", "view-transaction-form",
		"view-balances", "view-balance-form",
		"view-categories", "view-category-form", "view-notfound",
		"tx-add-btn", "b-add-btn", "c-add-btn",
	} {
		if !strings.Contains(html, `id="`+id+`"`) {
			t.Errorf("index.html has no #%s", id)
		}
	}
	// Uids are identifiers only; nothing renders them as visible text.
	for _, name := range []string{"router.js", "transactions.js", "balances.js", "categories.js", "style.css"} {
		b, err := fs.ReadFile(st, name)
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{`"uid muted"`, ".uid {", "uid.slice(", "textContent = e.uid", "textContent = b.uid", "textContent = c.uid"} {
			if strings.Contains(string(b), bad) {
				t.Errorf("%s still renders uids (%q)", name, bad)
			}
		}
	}
	// Absolute asset paths, so nested page URLs (/x/<uid>/edit) load them.
	if strings.Contains(html, `src="static/`) || strings.Contains(html, `href="static/`) {
		t.Error("relative static path in index.html")
	}
}
