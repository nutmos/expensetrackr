package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nutmos/expensetrackr/pkg/category"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// Categories are per user (issue #34): all of a user's categories
// live in one user_categories row as a JSON array. See docs/categories.md.

var (
	// ErrDuplicateCategory: another category of the same type (of the same
	// user) has this name (case-insensitive).
	ErrDuplicateCategory = errors.New("a category with this name and type already exists")
	// ErrCategoryInUse: the category is referenced by transactions, so it
	// cannot be deleted or change type.
	ErrCategoryInUse = errors.New("category is used by transactions")
	// ErrCategoryLimit: the user already has MaxCategoriesPerUser categories.
	ErrCategoryLimit = fmt.Errorf("category limit reached (at most %d categories per user)", MaxCategoriesPerUser)
	// errCategoryRowConflict: the user_categories row changed between the
	// read and the write (internal; the write is retried once).
	errCategoryRowConflict = errors.New("categories were changed concurrently; please retry")
)

// MaxCategoriesPerUser bounds the size of one user_categories row.
const MaxCategoriesPerUser = 500

// testHookCategoryRead, if set (tests only), runs inside the write
// transaction right after the row was read, to simulate a concurrent writer.
var testHookCategoryRead func(tx *sql.Tx, userUID string)

type rowQueryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// readUserCategories returns the stored categories of a user and the row
// version (0 = no row yet, i.e. no categories).
func readUserCategories(ctx context.Context, q rowQueryer, userUID string) ([]category.Category, int64, error) {
	var raw string
	var version int64
	err := q.QueryRowContext(ctx, `SELECT categories, version FROM user_categories WHERE user_uid = ?`, userUID).Scan(&raw, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return []category.Category{}, 0, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("read categories: %w", err)
	}
	cats := []category.Category{}
	if err := json.Unmarshal([]byte(raw), &cats); err != nil {
		return nil, 0, fmt.Errorf("decode categories of user %s: %w", userUID, err)
	}
	return cats, version, nil
}

// writeUserCategories stores cats for the user if the row is still at
// version (0: the row must not exist yet). Returns errCategoryRowConflict
// otherwise.
func writeUserCategories(ctx context.Context, tx *sql.Tx, userUID string, cats []category.Category, version int64) error {
	b, err := json.Marshal(cats)
	if err != nil {
		return err
	}
	now := ts(time.Now())
	var res sql.Result
	if version == 0 {
		res, err = tx.ExecContext(ctx, `INSERT INTO user_categories (user_uid, categories, version, created_at)
			VALUES (?, ?, 1, ?) ON CONFLICT (user_uid) DO NOTHING`, userUID, string(b), now)
	} else {
		res, err = tx.ExecContext(ctx, `UPDATE user_categories SET categories = ?, version = version + 1, updated_at = ?
			WHERE user_uid = ? AND version = ?`, string(b), now, userUID, version)
	}
	if isBusy(err) {
		// Another connection wrote the database after our read (WAL
		// snapshot is stale): same as a version mismatch.
		return errCategoryRowConflict
	}
	if err != nil {
		return fmt.Errorf("write categories: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return errCategoryRowConflict
	}
	return nil
}

// isBusy reports SQLITE_BUSY and its extended codes (e.g. BUSY_SNAPSHOT).
func isBusy(err error) bool {
	var se *sqlite.Error
	return errors.As(err, &se) && se.Code()&0xff == sqlite3.SQLITE_BUSY
}

// modifyCategories runs a read-modify-write of the user's category set in
// one SQL transaction with a version check on the row; on a version conflict
// it retries once from a fresh read. fn may change cats (returning the new
// slice) and may use tx for read-only checks.
func (s *Store) modifyCategories(ctx context.Context, userUID string, fn func(tx *sql.Tx, cats []category.Category) ([]category.Category, error)) error {
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		err = s.modifyCategoriesOnce(ctx, userUID, fn)
		if !errors.Is(err, errCategoryRowConflict) {
			return err
		}
	}
	return err
}

func (s *Store) modifyCategoriesOnce(ctx context.Context, userUID string, fn func(tx *sql.Tx, cats []category.Category) ([]category.Category, error)) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("categories: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	cats, version, err := readUserCategories(ctx, tx, userUID)
	if err != nil {
		return err
	}
	if testHookCategoryRead != nil {
		testHookCategoryRead(tx, userUID)
	}
	next, err := fn(tx, cats)
	if err != nil {
		return err
	}
	if err := writeUserCategories(ctx, tx, userUID, next, version); err != nil {
		return err
	}
	return tx.Commit()
}

// nameTaken reports whether another category (uid != skipUID) of the same
// type already has this name, compared case-insensitively (Unicode folding).
func nameTaken(cats []category.Category, c category.Category, skipUID string) bool {
	for _, o := range cats {
		if o.UID != skipUID && o.Type == c.Type && strings.EqualFold(o.Name, c.Name) {
			return true
		}
	}
	return false
}

func indexOfCategory(cats []category.Category, uid string) int {
	uid = strings.ToLower(uid)
	for i, c := range cats {
		if c.UID == uid {
			return i
		}
	}
	return -1
}

// CreateCategory adds a validated category to the user's set with a new
// UUID v4 uid.
func (s *Store) CreateCategory(ctx context.Context, userUID string, c *category.Category) error {
	uid, err := newUID()
	if err != nil {
		return err
	}
	out := *c
	out.UID = uid
	out.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	out.UpdatedAt = nil
	err = s.modifyCategories(ctx, userUID, func(_ *sql.Tx, cats []category.Category) ([]category.Category, error) {
		if nameTaken(cats, out, "") {
			return nil, ErrDuplicateCategory
		}
		if len(cats) >= MaxCategoriesPerUser {
			return nil, ErrCategoryLimit
		}
		return append(cats, out), nil
	})
	if err != nil {
		return err
	}
	*c = out
	return nil
}

// sortCategories orders expense first, then income, by name (case-insensitive).
func sortCategories(cats []category.Category) {
	rank := func(t category.Type) int {
		if t == category.Expense {
			return 1
		}
		return 2
	}
	sort.SliceStable(cats, func(i, j int) bool {
		a, b := cats[i], cats[j]
		if rank(a.Type) != rank(b.Type) {
			return rank(a.Type) < rank(b.Type)
		}
		if la, lb := strings.ToLower(a.Name), strings.ToLower(b.Name); la != lb {
			return la < lb
		}
		return a.CreatedAt < b.CreatedAt
	})
}

// ListCategories returns the user's categories (expense first, then income),
// by name. typ "" means all.
func (s *Store) ListCategories(ctx context.Context, userUID string, typ category.Type) ([]category.Category, error) {
	cats, _, err := readUserCategories(ctx, s.db, userUID)
	if err != nil {
		return nil, err
	}
	out := []category.Category{}
	for _, c := range cats {
		if typ == "" || c.Type == typ {
			out = append(out, c)
		}
	}
	sortCategories(out)
	return out, nil
}

// GetCategoryByUID returns one of the user's categories or ErrNotFound
// (also when the uid belongs to another user).
func (s *Store) GetCategoryByUID(ctx context.Context, userUID, uid string) (category.Category, error) {
	cats, _, err := readUserCategories(ctx, s.db, userUID)
	if err != nil {
		return category.Category{}, err
	}
	if i := indexOfCategory(cats, uid); i >= 0 {
		return cats[i], nil
	}
	return category.Category{}, ErrNotFound
}

func countCategoryRefs(ctx context.Context, tx *sql.Tx, uid string) (int, error) {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM transactions WHERE category_uid = ?`, strings.ToLower(uid)).Scan(&n)
	return n, err
}

// UpdateCategory loads the user's category uid, applies fn and stores the
// result (read-modify-write of the user's row, see modifyCategories). A type
// change is rejected with ErrCategoryInUse when any transaction references
// the category. UID/CreatedAt are preserved.
func (s *Store) UpdateCategory(ctx context.Context, userUID, uid string, fn func(cur category.Category) (category.Category, error)) (category.Category, error) {
	var result category.Category
	err := s.modifyCategories(ctx, userUID, func(tx *sql.Tx, cats []category.Category) ([]category.Category, error) {
		i := indexOfCategory(cats, uid)
		if i < 0 {
			return nil, ErrNotFound
		}
		cur := cats[i]
		next, err := fn(cur)
		if err != nil {
			return nil, err
		}
		if next.Type != cur.Type {
			n, err := countCategoryRefs(ctx, tx, cur.UID)
			if err != nil {
				return nil, err
			}
			if n > 0 {
				return nil, fmt.Errorf("%w (%d transaction(s)); its type cannot change", ErrCategoryInUse, n)
			}
		}
		if nameTaken(cats, next, cur.UID) {
			return nil, ErrDuplicateCategory
		}
		now := time.Now().UTC().Format(time.RFC3339)
		next.UID, next.CreatedAt, next.UpdatedAt = cur.UID, cur.CreatedAt, &now
		out := append([]category.Category(nil), cats...)
		out[i] = next
		result = next
		return out, nil
	})
	if err != nil {
		return category.Category{}, err
	}
	return result, nil
}

// DeleteCategory removes one of the user's categories. Returns
// ErrCategoryInUse if any transaction references it, ErrNotFound if the user
// has no such category.
func (s *Store) DeleteCategory(ctx context.Context, userUID, uid string) error {
	return s.modifyCategories(ctx, userUID, func(tx *sql.Tx, cats []category.Category) ([]category.Category, error) {
		i := indexOfCategory(cats, uid)
		if i < 0 {
			return nil, ErrNotFound
		}
		n, err := countCategoryRefs(ctx, tx, cats[i].UID)
		if err != nil {
			return nil, err
		}
		if n > 0 {
			return nil, fmt.Errorf("%w (%d transaction(s)); it cannot be deleted", ErrCategoryInUse, n)
		}
		out := append([]category.Category(nil), cats[:i]...)
		return append(out, cats[i+1:]...), nil
	})
}
