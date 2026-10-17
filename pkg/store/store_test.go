package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/nutmos/expensetrackr/pkg/balance"
	"github.com/nutmos/expensetrackr/pkg/transaction"
)

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// pragmaInt reads an integer PRAGMA (user_version, application_id) of the
// database at path.
func pragmaInt(t *testing.T, path, pragma string) int {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v int
	if err := db.QueryRow(`PRAGMA ` + pragma).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// rawDB creates a database file at path by running script directly.
func rawDB(t *testing.T, path, script string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(script); err != nil {
		t.Fatalf("setup script: %v", err)
	}
}

// schemaObjects lists every sqlite_master entry (type, name and normalized
// SQL) of the database at path, sorted by type and name.
func schemaObjects(t *testing.T, path string) map[string]string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT type, name, coalesce(sql, '') FROM sqlite_master`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var typ, name, ddl string
		if err := rows.Scan(&typ, &name, &ddl); err != nil {
			t.Fatal(err)
		}
		out[typ+" "+name] = strings.Join(strings.Fields(ddl), " ")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// baselineObjects are the user tables and indexes of the v1 baseline.
var baselineObjects = []string{
	"table transactions", "index idx_transactions_spent_at_unix", "index idx_transactions_balance_uid",
	"index idx_transactions_uid", "index idx_transactions_type", "index idx_transactions_category_uid",
	"index idx_transactions_to_balance_uid", "index idx_transactions_import_ref",
	"table balances", "index idx_balances_type", "index idx_balances_uid",
	"table users", "index idx_users_username", "index idx_users_email",
	"table user_identities", "index idx_user_identities_user_uid",
	"table sessions", "index idx_sessions_user_uid", "index idx_sessions_expires_at",
	"table user_categories", "table meta", "table fx_rates", "table fx_rate_lookups",
}

func TestFreshDatabaseGetsBaseline(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "fresh.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	b := mustBalance(t, balance.Input{Name: "Cash", Type: "payment_account", Currency: "SGD", Balance: dec("10")})
	if err := st.CreateBalance(ctx, &b); err != nil {
		t.Fatal(err)
	}
	e, err := transaction.CreateInput{Amount: "5", Currency: "SGD", BalanceUID: b.UID, SpentAt: "2026-10-07T08:00:00+08:00"}.Validate()
	if err != nil {
		t.Fatal(err)
	}
	e.Account = "Cash"
	if err := st.Create(ctx, &e); err != nil || e.ID != 1 {
		t.Fatalf("create on fresh db: id=%d err=%v", e.ID, err)
	}
	st.Close()

	if SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d, want 1 (baseline only)", SchemaVersion)
	}
	if v := pragmaInt(t, path, "user_version"); v != SchemaVersion {
		t.Errorf("user_version = %d, want %d", v, SchemaVersion)
	}
	if id := pragmaInt(t, path, "application_id"); id != applicationID {
		t.Errorf("application_id = %#x, want %#x", id, applicationID)
	}
	got := schemaObjects(t, path)
	var user []string
	for k := range got {
		if !strings.Contains(k, " sqlite_") {
			user = append(user, k)
		}
	}
	if len(user) != len(baselineObjects) {
		t.Errorf("schema objects = %v, want exactly %v", user, baselineObjects)
	}
	for _, k := range baselineObjects {
		if _, ok := got[k]; !ok {
			t.Errorf("missing %s", k)
		}
	}
	if ddl := got["table transactions"]; !strings.Contains(ddl, "'asset_purchase', 'balance_adjustment'") || !strings.Contains(ddl, "import_ref") {
		t.Errorf("transactions DDL: %s", ddl)
	}

	// Reopening a baseline database changes nothing.
	st, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	st.Close()
	if again := schemaObjects(t, path); fmt.Sprint(again) != fmt.Sprint(got) {
		t.Errorf("schema changed on reopen:\n got %v\nwant %v", again, got)
	}
	if v := pragmaInt(t, path, "user_version"); v != SchemaVersion {
		t.Errorf("user_version after reopen = %d", v)
	}
}

func TestEmptyFileGetsBaseline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.db")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open empty file: %v", err)
	}
	st.Close()
	if v := pragmaInt(t, path, "user_version"); v != SchemaVersion {
		t.Errorf("user_version = %d, want %d", v, SchemaVersion)
	}
}

// TestOpenRejectsUnsupportedSchema: databases from before the baseline reset
// (any user_version, application_id 0), foreign files and newer versions fail
// with ErrUnsupportedSchema and are left untouched.
func TestOpenRejectsUnsupportedSchema(t *testing.T) {
	const oldTable = `CREATE TABLE transactions (id INTEGER PRIMARY KEY AUTOINCREMENT, amount_minor INTEGER NOT NULL);
INSERT INTO transactions (amount_minor) VALUES (100);
`
	cases := []struct {
		name, script, wantMsg string
		fresh                 bool // start from a baseline database
	}{
		{name: "pre-reset v18", script: oldTable + `PRAGMA user_version = 18;`, wantMsg: "Delete the database file"},
		{name: "pre-reset v1", script: `CREATE TABLE expenses (id INTEGER PRIMARY KEY);
PRAGMA user_version = 1;`, wantMsg: "Delete the database file"},
		{name: "pre-reset v0 with tables", script: `CREATE TABLE expenses (id INTEGER PRIMARY KEY);`, wantMsg: "Delete the database file"},
		{name: "version but no tables", script: `PRAGMA user_version = 5;`, wantMsg: "Delete the database file"},
		{name: "foreign application id", script: `PRAGMA application_id = 42;`, wantMsg: "application_id 42"},
		{name: "newer version", fresh: true, script: fmt.Sprintf(`PRAGMA user_version = %d;`, SchemaVersion+1), wantMsg: "newer than this binary"},
		{name: "tagged but version 0", fresh: true, script: `PRAGMA user_version = 0;`, wantMsg: "schema version 0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "old.db")
			if c.fresh {
				st, err := Open(path)
				if err != nil {
					t.Fatal(err)
				}
				st.Close()
			}
			rawDB(t, path, c.script)
			before := schemaObjects(t, path)
			version := pragmaInt(t, path, "user_version")

			st, err := Open(path)
			if err == nil {
				st.Close()
				t.Fatal("Open succeeded, want ErrUnsupportedSchema")
			}
			if !errors.Is(err, ErrUnsupportedSchema) || !strings.Contains(err.Error(), c.wantMsg) || !strings.Contains(err.Error(), path) {
				t.Errorf("err = %v, want ErrUnsupportedSchema mentioning %q and the path", err, c.wantMsg)
			}
			if after := schemaObjects(t, path); fmt.Sprint(after) != fmt.Sprint(before) {
				t.Errorf("schema modified:\n got %v\nwant %v", after, before)
			}
			if v := pragmaInt(t, path, "user_version"); v != version {
				t.Errorf("user_version = %d, want %d (untouched)", v, version)
			}
		})
	}
}

func TestUpdateNotFound(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_, err = st.Update(context.Background(), 42, func(cur transaction.Transaction) (transaction.Transaction, error) { return cur, nil })
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}
