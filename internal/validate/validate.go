// Package validate holds the error types shared by the domain packages and
// mapped to HTTP status codes by the API layer.
package validate

import (
	"sort"
	"strings"
)

// ValidationError maps field names to human-readable problems (HTTP 422).
type ValidationError struct {
	Fields map[string]string `json:"fields"`
}

func (e *ValidationError) Error() string {
	keys := make([]string, 0, len(e.Fields))
	for k := range e.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+" "+e.Fields[k])
	}
	return "validation failed: " + strings.Join(parts, "; ")
}

// RequestError is a client error in the shape of the request itself (unknown
// field, wrong JSON type, empty patch). It maps to HTTP 400.
type RequestError struct{ Msg string }

func (e *RequestError) Error() string { return e.Msg }
