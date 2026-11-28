// Package helper holds small, dependency-free helpers shared across packages.
package helper

import "strings"

// WithSearchPath adds search_path=schema to a postgres URL
// (postgres:// or postgresql://) or to a key=value connection string.
func WithSearchPath(url, schema string) string {
	if strings.HasPrefix(url, "postgres://") || strings.HasPrefix(url, "postgresql://") {
		sep := "?"
		if strings.Contains(url, "?") {
			sep = "&"
		}
		return url + sep + "search_path=" + schema
	}
	return url + " search_path=" + schema
}
