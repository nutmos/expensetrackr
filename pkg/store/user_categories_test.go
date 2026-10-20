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
	food := category.Category{Name: "Food", Type: category.TypeExpense}
	if err := st.CreateCategory(ctx, a, &food); err != nil || len(food.UID) != 36 {
		t.Fatalf("create: %+v %v", food, err)
	}
	if err := st.CreateCategory(ctx, a, &category.Category{Name: "FOOD", Type: category.TypeExpense}); !errors.Is(err, ErrDuplicateCategory) {
		t.Errorf("dup: %v", err)
	}
	if err := st.CreateCategory(ctx, a, &category.Category{Name: "food", Type: category.TypeIncome}); err != nil {
		t.Errorf("same name other type: %v", err)
	}
	// Bob may reuse Alice's names, and cannot see or touch her categories.
	if err := st.CreateCategory(ctx, b, &category.Category{Name: "Food", Type: category.TypeExpense}); err != nil {
		t.Errorf("bob same name: %v", err)
	}
	if _, err := st.GetCategoryByUID(ctx, b, string(food.UID)); !errors.Is(err, ErrNotFound) {
		t.Errorf("bob get alice's: %v", err)
	}
	if err := st.DeleteCategory(ctx, b, string(food.UID)); !errors.Is(err, ErrNotFound) {
		t.Errorf("bob delete alice's: %v", err)
	}
	if _, err := st.UpdateCategory(ctx, b, string(food.UID), func(c category.Category) (category.Category, error) { return c, nil }); !errors.Is(err, ErrNotFound) {
		t.Errorf("bob update alice's: %v", err)
	}
	if c, _ := st.ListCategories(ctx, b, ""); len(c) != 1 {
		t.Errorf("bob list: %v", c)
	}

	// Update: rename, keep uid/created_at, uniqueness on rename.
	up, err := st.UpdateCategory(ctx, a, strings.ToUpper(string(food.UID)), func(c category.Category) (category.Category, error) {
		c.Name, c.Description = "Meals", "d"
		return c, nil
	})
	if err != nil || up.UID != food.UID || up.CreatedAt != food.CreatedAt || up.UpdatedAt == nil || up.Name != "Meals" {
		t.Fatalf("update: %+v %v", up, err)
	}
	tr := category.Category{Name: "Transport", Type: category.TypeExpense}
	if err := st.CreateCategory(ctx, a, &tr); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateCategory(ctx, a, string(tr.UID), func(c category.Category) (category.Category, error) {
		c.Name = "meals"
		return c, nil
	}); !errors.Is(err, ErrDuplicateCategory) {
		t.Errorf("rename to dup: %v", err)
	}
	if list, _ := st.ListCategories(ctx, a, category.TypeIncome); len(list) != 1 || list[0].Name != "food" {
		t.Errorf("income filter: %v", list)
	}
	if list, _ := st.ListCategories(ctx, a, ""); len(list) != 3 || list[0].Name != "Meals" || list[1].Name != "Transport" || list[2].Type != category.TypeIncome {
		t.Errorf("order: %v", list)
	}
	if err := st.DeleteCategory(ctx, a, string(tr.UID)); err != nil {
		t.Errorf("delete: %v", err)
	}
	if _, err := st.GetCategoryByUID(ctx, a, string(tr.UID)); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted still there: %v", err)
	}

	// Limit.
	for i := 0; ; i++ {
		err := st.CreateCategory(ctx, b, &category.Category{Name: fmt.Sprintf("c%d", i), Type: category.TypeExpense})
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
	if err := st.db.QueryRow(`SELECT id FROM users WHERE uid = ?`, b).Scan(&bid); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteUser(ctx, bid); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := st.db.QueryRow(`SELECT count(*) FROM user_categories WHERE user_uid = ?`, b).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("categories row of deleted user remains")
	}
}

func TestUserCategoriesInUse(t *testing.T) {
	ctx := context.Background()
	st, u := newUserStore(t, "alice")
	food := category.Category{Name: "Food", Type: category.TypeExpense}
	if err := st.CreateCategory(ctx, u[0], &food); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`INSERT INTO transactions (uid, type, amount_minor, amount_scale, currency, balance_uid, account, category_uid, spent_at, spent_at_unix, created_at)
		VALUES ('cccccccc-cccc-4ccc-8ccc-cccccccccccc', 'expense', 100, 2, 'THB', 'dddddddd-dddd-4ddd-8ddd-dddddddddddd', 'Cash', ?, '2026-01-05T00:00:00Z', 1, '2026-01-05T00:00:00Z')`, food.UID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteCategory(ctx, u[0], string(food.UID)); !errors.Is(err, ErrCategoryInUse) {
		t.Errorf("delete: %v", err)
	}
	if _, err := st.UpdateCategory(ctx, u[0], string(food.UID), func(c category.Category) (category.Category, error) {
		c.Type = category.TypeIncome
		return c, nil
	}); !errors.Is(err, ErrCategoryInUse) {
		t.Errorf("type change: %v", err)
	}
	if _, err := st.UpdateCategory(ctx, u[0], string(food.UID), func(c category.Category) (category.Category, error) {
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
	first := category.Category{Name: "First", Type: category.TypeExpense}
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
	second := category.Category{Name: "Second", Type: category.TypeExpense}
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
	if err := st.CreateCategory(ctx, a, &category.Category{Name: "Third", Type: category.TypeExpense}); !errors.Is(err, errCategoryRowConflict) {
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
			errs <- st.CreateCategory(ctx, u[0], &category.Category{Name: fmt.Sprintf("cat %d", i), Type: category.TypeExpense})
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
