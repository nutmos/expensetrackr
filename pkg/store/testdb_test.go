package store

import (
	"os"
	"testing"
)

// testURL is storetest.URL for this package's own tests (which can't import
// storetest: it imports this package). See pkg/store/storetest.
func testURL(t testing.TB) string {
	t.Helper()
	base := os.Getenv("EXPENSE_TEST_DATABASE_URL")
	if base == "" {
		if os.Getenv("CI") == "true" {
			t.Fatal("EXPENSE_TEST_DATABASE_URL is not set; CI must provide a Postgres test database")
		}
		t.Skip("EXPENSE_TEST_DATABASE_URL is not set; run `make db-up` and `make test` (or set it) to run database tests")
	}
	url, drop, err := CreateTestSchema(base)
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

// openTestStore returns a store in a fresh schema, closed when the test ends.
func openTestStore(t testing.TB) *Store {
	t.Helper()
	st, err := Open(testURL(t))
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}
