package api

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"regexp"
)

// staticRef matches a /static/ asset reference in index.html, e.g.
// src="/static/router.js" (group 1: up to the path, group 2: the path).
var staticRef = regexp.MustCompile(`((?:src|href)="/static/)([^"?#]+)"`)

// versionedIndex returns index.html with a content hash appended to every
// /static/ reference (src="/static/router.js?v=1a2b3c4d5e6f"). A deploy that
// changes a file changes its URL, so a browser or CDN (Cloudflare) cache can
// never pair a new index.html with an older script: that mismatch left the
// router without its routes and every page showed "Page not found".
// References to files that don't exist are left as they are.
func versionedIndex(fsys fs.FS) ([]byte, error) {
	b, err := fs.ReadFile(fsys, "index.html")
	if err != nil {
		return nil, err
	}
	return staticRef.ReplaceAllFunc(b, func(m []byte) []byte {
		sub := staticRef.FindSubmatch(m)
		data, err := fs.ReadFile(fsys, string(sub[2]))
		if err != nil {
			return m
		}
		sum := sha256.Sum256(data)
		return fmt.Appendf(nil, `%s%s?v=%x"`, sub[1], sub[2], sum[:6])
	}), nil
}
