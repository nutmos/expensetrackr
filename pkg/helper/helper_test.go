package helper

import "testing"

func TestWithSearchPath(t *testing.T) {
	for _, c := range []struct{ url, want string }{
		{"postgres://u:p@h/db", "postgres://u:p@h/db?search_path=s"},
		{"postgresql://u:p@h/db", "postgresql://u:p@h/db?search_path=s"},
		{"postgres://u:p@h/db?sslmode=disable", "postgres://u:p@h/db?sslmode=disable&search_path=s"},
		{"host=h dbname=db", "host=h dbname=db search_path=s"},
		{"postgres:/", "postgres:/ search_path=s"},
	} {
		if got := WithSearchPath(c.url, "s"); got != c.want {
			t.Errorf("WithSearchPath(%q) = %q, want %q", c.url, got, c.want)
		}
	}
}
