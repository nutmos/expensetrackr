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

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"":              "",
		"  Expense\t\n": "expense",
		"BALANCE_ADJ":   "balance_adj",
		"Ünï":           "ünï",
	} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEnvOr(t *testing.T) {
	const key = "EXPENSETRACKR_HELPER_TEST_ENV"
	t.Setenv(key, "")
	if got := EnvOr(key, "def"); got != "def" {
		t.Errorf("empty: EnvOr = %q, want def", got)
	}
	t.Setenv(key, "set")
	if got := EnvOr(key, "def"); got != "set" {
		t.Errorf("set: EnvOr = %q, want set", got)
	}
}
