// Package helper holds small, dependency-free helpers shared across packages.
package helper

import (
	"os"
	"strings"
)

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

// Normalize trims surrounding whitespace and lower-cases s, the form in which
// enum-like client input (types, directions, usernames, emails) is compared.
func Normalize(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// EnvOr returns the environment variable key, or def when it is unset or empty.
func EnvOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
