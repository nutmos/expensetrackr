package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" driver for rawDB

	"github.com/nutmos/expensetrackr/pkg/balance"
	"github.com/nutmos/expensetrackr/pkg/transaction"
)

// rawDB opens a plain connection to the database at url (for direct SQL
// in tests), closed when the test ends.
func rawDB(t *testing.T, url string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// schemaObjects lists the tables and indexes of the current schema of the
// database at url, as "table <name>" / "index <name>" -> definition.
func schemaObjects(t *testing.T, url string) map[string]string {
	t.Helper()
	rows, err := rawDB(t, url).Query(`
		SELECT 'table ' || tablename, '' FROM pg_tables WHERE schemaname = current_schema()
		UNION ALL
		SELECT 'index ' || indexname, indexdef FROM pg_indexes WHERE schemaname = current_schema()`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, def string
		if err := rows.Scan(&name, &def); err != nil {
			t.Fatal(err)
		}
		out[name] = def
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// schemaVersion is the highest version recorded in schema_migrations.
func schemaVersion(t *testing.T, url string) int {
	t.Helper()
	var v int
	if err := rawDB(t, url).QueryRow(`SELECT coalesce(max(version), 0) FROM schema_migrations`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// baselineTables are the tables of the v1 schema (plus schema_migrations).
var baselineTables = []string{
	"users", "user_identities", "sessions", "api_tokens", "user_categories",
	"balances", "transactions", "fx_rates", "fx_rate_lookups", "schema_migrations",
}

// baselineIndexes are the explicitly named indexes and unique constraints
// the store relies on (constraint names are mapped to errors in pgerr.go).
var baselineIndexes = []string{
	"users_username_uq", "users_email_uq", "user_identities_provider_subject_uq",
	"balances_user_name_uq", "transactions_import_ref_uq", "transactions_user_spent_idx",
}

func TestFreshDatabaseGetsBaseline(t *testing.T) {
	ctx := context.Background()
	url := testURL(t)
	st, err := Open(url)
	if err != nil {
		t.Fatal(err)
	}
	owner := seedOwner(t, st)
	b := mustBalance(t, balance.Input{Name: "Cash", Type: "payment_account", Currency: "SGD", Balance: dec("10")})
	if err := st.CreateBalance(ctx, owner, &b); err != nil {
		t.Fatal(err)
	}
	e, err := transaction.CreateInput{Amount: "5", Currency: "SGD", BalanceUID: string(b.UID), SpentAt: "2026-10-07T08:00:00+08:00"}.Validate()
	if err != nil {
		t.Fatal(err)
	}
	e.Account = "Cash"
	if err := st.Create(ctx, owner, &e); err != nil || e.ID != 1 {
		t.Fatalf("create on fresh db: id=%d err=%v", e.ID, err)
	}
	st.Close()

	if SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d, want 1 (Postgres baseline)", SchemaVersion)
	}
	if v := schemaVersion(t, url); v != SchemaVersion {
		t.Errorf("schema version = %d, want %d", v, SchemaVersion)
	}
	got := schemaObjects(t, url)
	var tables []string
	for k := range got {
		if strings.HasPrefix(k, "table ") {
			tables = append(tables, strings.TrimPrefix(k, "table "))
		}
	}
	slices.Sort(tables)
	want := slices.Clone(baselineTables)
	slices.Sort(want)
	if !slices.Equal(tables, want) {
		t.Errorf("tables = %v, want %v", tables, want)
	}
	for _, ix := range baselineIndexes {
		if _, ok := got["index "+ix]; !ok {
			t.Errorf("missing index %s", ix)
		}
	}

	// Reopening an up-to-date database changes nothing.
	st, err = Open(url)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	st.Close()
	if again := schemaObjects(t, url); fmt.Sprint(again) != fmt.Sprint(got) {
		t.Errorf("schema changed on reopen:\n got %v\nwant %v", again, got)
	}
	var rows int
	if err := rawDB(t, url).QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&rows); err != nil || rows != SchemaVersion {
		t.Errorf("schema_migrations rows = %d (%v), want %d", rows, err, SchemaVersion)
	}
}

// TestOpenRejectsNewerSchema: a database from a newer build fails with
// ErrUnsupportedSchema and is left untouched.
func TestOpenRejectsNewerSchema(t *testing.T) {
	url := testURL(t)
	st, err := Open(url)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	if _, err := rawDB(t, url).Exec(`INSERT INTO schema_migrations (version) VALUES ($1)`, SchemaVersion+1); err != nil {
		t.Fatal(err)
	}
	before := schemaObjects(t, url)
	st, err = Open(url)
	if err == nil {
		st.Close()
		t.Fatal("Open succeeded, want ErrUnsupportedSchema")
	}
	if !errors.Is(err, ErrUnsupportedSchema) || !strings.Contains(err.Error(), "newer than this binary") {
		t.Errorf("err = %v, want ErrUnsupportedSchema (newer than this binary)", err)
	}
	if after := schemaObjects(t, url); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Errorf("schema modified:\n got %v\nwant %v", after, before)
	}
	if v := schemaVersion(t, url); v != SchemaVersion+1 {
		t.Errorf("version = %d, want %d (untouched)", v, SchemaVersion+1)
	}
}

// TestConcurrentOpenMigratesOnce: servers starting at the same time against
// an empty database don't race (the advisory lock in migrate).
func TestConcurrentOpenMigratesOnce(t *testing.T) {
	url := testURL(t)
	const n = 4
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			st, err := Open(url)
			if err == nil {
				st.Close()
			}
			errs <- err
		}()
	}
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Errorf("concurrent Open: %v", err)
		}
	}
	var rows int
	if err := rawDB(t, url).QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&rows); err != nil || rows != SchemaVersion {
		t.Errorf("schema_migrations rows = %d (%v), want %d", rows, err, SchemaVersion)
	}
}

// TestOpenUnreachable: a database that can't be reached is a clear error that
// never shows the password.
func TestOpenUnreachable(t *testing.T) {
	_, err := Open("postgres://someone:s3cret-pw@127.0.0.1:1/nowhere?sslmode=disable&connect_timeout=2")
	if err == nil {
		t.Fatal("Open succeeded")
	}
	if strings.Contains(err.Error(), "s3cret-pw") || !strings.Contains(err.Error(), "someone@127.0.0.1:1/nowhere") {
		t.Errorf("err = %v", err)
	}
	if _, err := Open("::not a url::"); err == nil {
		t.Error("malformed url accepted")
	}
}

func TestUpdateNotFound(t *testing.T) {
	st := openTestStore(t)
	_, err := st.UpdateWith(context.Background(), "00000000-0000-4000-8000-000000000000", 42, WriteOptions{}, func(cur transaction.Transaction) (transaction.Transaction, error) { return cur, nil })
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}
