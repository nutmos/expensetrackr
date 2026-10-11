package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nutmos/expensetrackr/pkg/category"
	"github.com/nutmos/expensetrackr/pkg/user"
)

const (
	catFood   = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	catSalary = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	userOld   = "11111111-1111-4111-8111-111111111111" // oldest, no password
	userPw    = "22222222-2222-4222-8222-222222222222" // oldest with password
	userPw2   = "33333333-3333-4333-8333-333333333333"
)

// downgradeToV14 turns a fresh database into a v14 one (old categories
// table, no user_categories/meta), then runs script.
func downgradeToV14(t *testing.T, path, script string) {
	t.Helper()
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	rawDB(t, path, "DROP TABLE user_categories;\nDROP TABLE meta;\n"+categoriesSchema+script+"\nPRAGMA user_version = 14;")
}

const v14Categories = `
INSERT INTO categories (uid, name, type, description, created_at, updated_at) VALUES
 ('` + catFood + `', 'Food', 'expense', 'eat', '2026-01-01T00:00:00Z', '2026-01-02T00:00:00Z'),
 ('` + catSalary + `', 'Salary', 'income', '', '2026-01-03T00:00:00Z', NULL);
INSERT INTO transactions (uid, type, amount_minor, amount_scale, currency, balance_uid, account, category_uid, spent_at, spent_at_unix, created_at)
VALUES ('cccccccc-cccc-4ccc-8ccc-cccccccccccc', 'expense', 100, 2, 'THB', 'dddddddd-dddd-4ddd-8ddd-dddddddddddd', 'Cash', '` + catFood + `', '2026-01-05T00:00:00Z', 1767571200, '2026-01-05T00:00:00Z');
`

const v14Users = `
INSERT INTO users (uid, username, display_name, created_at) VALUES ('` + userOld + `', 'old', 'Old', '2025-01-01T00:00:00Z');
INSERT INTO users (uid, username, display_name, password_hash, created_at) VALUES ('` + userPw + `', 'pwuser', 'Pw', '$2a$04$x', '2025-02-01T00:00:00Z');
INSERT INTO users (uid, username, display_name, password_hash, created_at) VALUES ('` + userPw2 + `', 'pwtwo', 'Pw2', '$2a$04$x', '2025-03-01T00:00:00Z');
`

func checkMigratedCats(t *testing.T, st *Store, owner string) {
	t.Helper()
	ctx := context.Background()
	cats, err := st.ListCategories(ctx, owner, "")
	if err != nil || len(cats) != 2 {
		t.Fatalf("owner categories: %v %v", cats, err)
	}
	f := cats[0]
	if f.UID != catFood || f.Name != "Food" || f.Type != category.Expense || f.Description != "eat" ||
		f.CreatedAt != "2026-01-01T00:00:00Z" || f.UpdatedAt == nil || *f.UpdatedAt != "2026-01-02T00:00:00Z" {
		t.Errorf("food not preserved: %+v", f)
	}
	if cats[1].UID != catSalary || cats[1].UpdatedAt != nil {
		t.Errorf("salary not preserved: %+v", cats[1])
	}
	// The transaction still links to the category, and it is in use.
	items, err := st.List(ctx, ListFilter{})
	if err != nil || len(items) != 1 || items[0].CategoryUID == nil || *items[0].CategoryUID != catFood {
		t.Fatalf("transactions: %+v %v", items, err)
	}
	if err := st.DeleteCategory(ctx, owner, catFood); !errors.Is(err, ErrCategoryInUse) {
		t.Errorf("delete in-use migrated category: %v", err)
	}
}

func dbIntegrity(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var ok string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&ok); err != nil || ok != "ok" {
		t.Errorf("integrity_check: %q %v", ok, err)
	}
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Errorf("foreign_key_check reported violations")
	}
}

func TestMigrationV14ToV15WithUsers(t *testing.T) {
	for _, rerun := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "v14.db")
		downgradeToV14(t, path, v14Users+v14Categories)
		if rerun {
			// Partly applied before (tables exist, one category already moved):
			// the migration must not duplicate.
			rawDB(t, path, userCategoriesSchema+`INSERT INTO user_categories (user_uid, categories, created_at)
				VALUES ('`+userPw+`', '[{"uid":"`+catFood+`","name":"Food","type":"expense","description":"eat","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}]', '2026-01-01T00:00:00Z');`)
		}
		st, err := Open(path)
		if err != nil {
			t.Fatalf("open v14: %v", err)
		}
		checkMigratedCats(t, st, userPw) // oldest user WITH a password
		for _, other := range []string{userOld, userPw2} {
			if c, _ := st.ListCategories(context.Background(), other, ""); len(c) != 0 {
				t.Errorf("user %s got categories: %v", other, c)
			}
		}
		st.Close()
		assertCurrentSchema(t, path)
		dbIntegrity(t, path)
	}
}

func TestMigrationV14ToV15OnlyPasswordlessUser(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v14.db")
	downgradeToV14(t, path, `INSERT INTO users (uid, username, display_name, created_at) VALUES ('`+userOld+`', 'old', 'Old', '2025-01-01T00:00:00Z');`+v14Categories)
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	checkMigratedCats(t, st, userOld)
	st.Close()
	assertCurrentSchema(t, path)
}

func TestMigrationV14ToV15NoUsersStashesForFirstRegistrant(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v14.db")
	downgradeToV14(t, path, v14Categories)
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	assertCurrentSchema(t, path)
	dbIntegrity(t, path)
	var n int
	if err := st.db.QueryRow(`SELECT json_array_length(value) FROM meta WHERE key = ?`, unclaimedCategoriesKey).Scan(&n); err != nil || n != 2 {
		t.Fatalf("stash: %d %v", n, err)
	}
	u := user.User{Username: strp("first"), DisplayName: "First", Preferences: []byte("{}")}
	if err := st.RegisterUser(ctx, &u, "$2a$04$hash", true); err != nil {
		t.Fatal(err)
	}
	checkMigratedCats(t, st, u.UID)
	if err := st.db.QueryRow(`SELECT count(*) FROM meta`).Scan(&n); err != nil || n != 0 {
		t.Errorf("stash not cleared: %d %v", n, err)
	}
	u2 := user.User{Username: strp("second"), DisplayName: "Second", Preferences: []byte("{}")}
	if err := st.RegisterUser(ctx, &u2, "$2a$04$hash", false); err != nil {
		t.Fatal(err)
	}
	if c, _ := st.ListCategories(ctx, u2.UID, ""); len(c) != 0 {
		t.Errorf("second user got categories: %v", c)
	}
}

func newUserStore(t *testing.T, names ...string) (*Store, []string) {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "cats.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	var uids []string
	for _, n := range names {
		u := user.User{Username: strp(n), DisplayName: n, Preferences: []byte("{}")}
		if err := st.RegisterUser(context.Background(), &u, "$2a$04$hash", false); err != nil {
			t.Fatal(err)
		}
		uids = append(uids, u.UID)
	}
	return st, uids
}

func TestUserCategoriesCRUDIsolationAndLimits(t *testing.T) {
	ctx := context.Background()
	st, u := newUserStore(t, "alice", "bob")
	a, b := u[0], u[1]

	if c, err := st.ListCategories(ctx, a, ""); err != nil || len(c) != 0 {
		t.Fatalf("empty list: %v %v", c, err)
	}
	food := category.Category{Name: "Food", Type: category.Expense}
	if err := st.CreateCategory(ctx, a, &food); err != nil || len(food.UID) != 36 {
		t.Fatalf("create: %+v %v", food, err)
	}
	if err := st.CreateCategory(ctx, a, &category.Category{Name: "FOOD", Type: category.Expense}); !errors.Is(err, ErrDuplicateCategory) {
		t.Errorf("dup: %v", err)
	}
	if err := st.CreateCategory(ctx, a, &category.Category{Name: "food", Type: category.Income}); err != nil {
		t.Errorf("same name other type: %v", err)
	}
	// Bob may reuse Alice's names, and cannot see or touch her categories.
	if err := st.CreateCategory(ctx, b, &category.Category{Name: "Food", Type: category.Expense}); err != nil {
		t.Errorf("bob same name: %v", err)
	}
	if _, err := st.GetCategoryByUID(ctx, b, food.UID); !errors.Is(err, ErrNotFound) {
		t.Errorf("bob get alice's: %v", err)
	}
	if err := st.DeleteCategory(ctx, b, food.UID); !errors.Is(err, ErrNotFound) {
		t.Errorf("bob delete alice's: %v", err)
	}
	if _, err := st.UpdateCategory(ctx, b, food.UID, func(c category.Category) (category.Category, error) { return c, nil }); !errors.Is(err, ErrNotFound) {
		t.Errorf("bob update alice's: %v", err)
	}
	if c, _ := st.ListCategories(ctx, b, ""); len(c) != 1 {
		t.Errorf("bob list: %v", c)
	}

	// Update: rename, keep uid/created_at, uniqueness on rename.
	up, err := st.UpdateCategory(ctx, a, strings.ToUpper(food.UID), func(c category.Category) (category.Category, error) {
		c.Name, c.Description = "Meals", "d"
		return c, nil
	})
	if err != nil || up.UID != food.UID || up.CreatedAt != food.CreatedAt || up.UpdatedAt == nil || up.Name != "Meals" {
		t.Fatalf("update: %+v %v", up, err)
	}
	tr := category.Category{Name: "Transport", Type: category.Expense}
	st.CreateCategory(ctx, a, &tr)
	if _, err := st.UpdateCategory(ctx, a, tr.UID, func(c category.Category) (category.Category, error) {
		c.Name = "meals"
		return c, nil
	}); !errors.Is(err, ErrDuplicateCategory) {
		t.Errorf("rename to dup: %v", err)
	}
	if list, _ := st.ListCategories(ctx, a, category.Income); len(list) != 1 || list[0].Name != "food" {
		t.Errorf("income filter: %v", list)
	}
	if list, _ := st.ListCategories(ctx, a, ""); len(list) != 3 || list[0].Name != "Meals" || list[1].Name != "Transport" || list[2].Type != category.Income {
		t.Errorf("order: %v", list)
	}
	if err := st.DeleteCategory(ctx, a, tr.UID); err != nil {
		t.Errorf("delete: %v", err)
	}
	if _, err := st.GetCategoryByUID(ctx, a, tr.UID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted still there: %v", err)
	}

	// Limit.
	for i := 0; ; i++ {
		err := st.CreateCategory(ctx, b, &category.Category{Name: fmt.Sprintf("c%d", i), Type: category.Expense})
		if errors.Is(err, ErrCategoryLimit) {
			if c, _ := st.ListCategories(ctx, b, ""); len(c) != MaxCategoriesPerUser {
				t.Errorf("limit hit at %d", len(c))
			}
			break
		}
		if err != nil || i > MaxCategoriesPerUser {
			t.Fatalf("create %d: %v", i, err)
		}
	}

	// Deleting a user cascades its categories row.
	var bid int64
	st.db.QueryRow(`SELECT id FROM users WHERE uid = ?`, b).Scan(&bid)
	if err := st.DeleteUser(ctx, bid); err != nil {
		t.Fatal(err)
	}
	var n int
	st.db.QueryRow(`SELECT count(*) FROM user_categories WHERE user_uid = ?`, b).Scan(&n)
	if n != 0 {
		t.Errorf("categories row of deleted user remains")
	}
}

func TestUserCategoriesInUse(t *testing.T) {
	ctx := context.Background()
	st, u := newUserStore(t, "alice")
	food := category.Category{Name: "Food", Type: category.Expense}
	st.CreateCategory(ctx, u[0], &food)
	if _, err := st.db.Exec(`INSERT INTO transactions (uid, type, amount_minor, amount_scale, currency, balance_uid, account, category_uid, spent_at, spent_at_unix, created_at)
		VALUES ('cccccccc-cccc-4ccc-8ccc-cccccccccccc', 'expense', 100, 2, 'THB', 'dddddddd-dddd-4ddd-8ddd-dddddddddddd', 'Cash', ?, '2026-01-05T00:00:00Z', 1, '2026-01-05T00:00:00Z')`, food.UID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteCategory(ctx, u[0], food.UID); !errors.Is(err, ErrCategoryInUse) {
		t.Errorf("delete: %v", err)
	}
	if _, err := st.UpdateCategory(ctx, u[0], food.UID, func(c category.Category) (category.Category, error) {
		c.Type = category.Income
		return c, nil
	}); !errors.Is(err, ErrCategoryInUse) {
		t.Errorf("type change: %v", err)
	}
	if _, err := st.UpdateCategory(ctx, u[0], food.UID, func(c category.Category) (category.Category, error) {
		c.Name = "Groceries"
		return c, nil
	}); err != nil {
		t.Errorf("rename in-use: %v", err)
	}
}

func TestUserCategoriesVersionConflictRetry(t *testing.T) {
	ctx := context.Background()
	st, u := newUserStore(t, "alice")
	a := u[0]
	first := category.Category{Name: "First", Type: category.Expense}
	if err := st.CreateCategory(ctx, a, &first); err != nil {
		t.Fatal(err)
	}
	// Simulate another process committing between our read and our write
	// (a second connection to the same WAL database): our write sees a stale
	// row, is retried once from a fresh read and succeeds without losing the
	// other writer's change.
	other, err := sql.Open("sqlite", "file:"+st.path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	calls := 0
	testHookCategoryRead = func(_ *sql.Tx, userUID string) {
		calls++
		if calls == 1 {
			if _, err := other.Exec(`UPDATE user_categories SET version = version + 1,
				categories = json_insert(categories, '$[#]', json('{"uid":"eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee","name":"Other","type":"expense","description":"","created_at":"2026-01-01T00:00:00Z","updated_at":null}'))
				WHERE user_uid = ?`, userUID); err != nil {
				t.Error(err)
			}
		}
	}
	defer func() { testHookCategoryRead = nil }()
	second := category.Category{Name: "Second", Type: category.Expense}
	if err := st.CreateCategory(ctx, a, &second); err != nil {
		t.Fatalf("create with one conflict: %v", err)
	}
	if calls != 2 {
		t.Errorf("attempts = %d, want 2", calls)
	}
	if c, _ := st.ListCategories(ctx, a, ""); len(c) != 3 {
		t.Errorf("after retry: %v", c)
	}
	// Two conflicts in a row: give up with a conflict error, nothing lost.
	testHookCategoryRead = func(_ *sql.Tx, userUID string) {
		if _, err := other.Exec(`UPDATE user_categories SET version = version + 1 WHERE user_uid = ?`, userUID); err != nil {
			t.Error(err)
		}
	}
	if err := st.CreateCategory(ctx, a, &category.Category{Name: "Third", Type: category.Expense}); !errors.Is(err, errCategoryRowConflict) {
		t.Errorf("persistent conflict: %v", err)
	}
}

func TestUserCategoriesConcurrentWrites(t *testing.T) {
	ctx := context.Background()
	st, u := newUserStore(t, "alice")
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- st.CreateCategory(ctx, u[0], &category.Category{Name: fmt.Sprintf("cat %d", i), Type: category.Expense})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	if c, _ := st.ListCategories(ctx, u[0], ""); len(c) != 40 {
		t.Errorf("concurrent creates kept %d of 40", len(c))
	}
}
