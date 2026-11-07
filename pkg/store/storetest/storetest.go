// Package storetest gives tests a private Postgres database: a fresh,
// uniquely named schema in the test database, dropped when the test ends.
//
// The test database comes from EXPENSE_TEST_DATABASE_URL (make db-up starts
// one; the Makefile sets the variable). Without it the test is skipped, so
// plain `go test` works without Docker; in CI (CI=true) a missing URL is a
// failure instead, so the database tests can never silently not run.
package storetest

import (
	"os"
	"testing"

	"github.com/nutmos/expensetrackr/pkg/store"
)

// EnvURL names the variable holding the test database URL.
const EnvURL = "EXPENSE_TEST_DATABASE_URL"

// URL creates a fresh schema and returns a database URL that uses it. Every
// store opened with it sees the same data, so a test can reopen it.
func URL(t testing.TB) string {
	t.Helper()
	base := os.Getenv(EnvURL)
	if base == "" {
		if os.Getenv("CI") == "true" {
			t.Fatalf("%s is not set; CI must provide a Postgres test database", EnvURL)
		}
		t.Skipf("%s is not set; run `make db-up` and `make test` (or set it) to run database tests", EnvURL)
	}
	url, drop, err := store.CreateTestSchema(base)
	if err != nil {
		t.Fatalf("test database: %v", err)
	}
	t.Cleanup(func() {
		if err := drop(); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
	})
	return url
}

// Open returns a store in a fresh schema, closed when the test ends.
func Open(t testing.TB) *store.Store {
	t.Helper()
	st, err := store.Open(URL(t))
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}
