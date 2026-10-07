package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"expense-service/internal/category"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

var (
	// ErrDuplicateCategory: another category of the same type has this name
	// (case-insensitive).
	ErrDuplicateCategory = errors.New("a category with this name and type already exists")
	// ErrCategoryInUse: the category is referenced by transactions, so it
	// cannot be deleted or change type.
	ErrCategoryInUse = errors.New("category is used by transactions")
)

const categoryCols = `id, uid, name, type, description, created_at, updated_at`

func mapCategoryErr(err error) error {
	var se *sqlite.Error
	if errors.As(err, &se) && se.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE {
		return ErrDuplicateCategory
	}
	return err
}

// CreateCategory inserts a validated category with a new UUID v4 uid.
func (s *Store) CreateCategory(ctx context.Context, c *category.Category) error {
	uid, err := newUID()
	if err != nil {
		return err
	}
	c.UID = uid
	c.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	c.UpdatedAt = nil
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO categories (uid, name, type, description, created_at) VALUES (?, ?, ?, ?, ?)`,
		c.UID, c.Name, string(c.Type), c.Description, c.CreatedAt)
	if err != nil {
		if errors.Is(mapCategoryErr(err), ErrDuplicateCategory) {
			return ErrDuplicateCategory
		}
		return fmt.Errorf("insert category: %w", err)
	}
	c.ID, err = res.LastInsertId()
	return err
}

// ListCategories returns categories (expense first, then income), by name.
// typ "" means all.
func (s *Store) ListCategories(ctx context.Context, typ category.Type) ([]category.Category, error) {
	q := `SELECT ` + categoryCols + ` FROM categories`
	var args []any
	if typ != "" {
		q += ` WHERE type = ?`
		args = append(args, string(typ))
	}
	q += ` ORDER BY CASE type WHEN 'expense' THEN 1 ELSE 2 END, name COLLATE NOCASE, id`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list categories: %w", err)
	}
	defer rows.Close()
	out := []category.Category{}
	for rows.Next() {
		c, err := scanCategory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetCategoryByUID returns one category or ErrNotFound.
func (s *Store) GetCategoryByUID(ctx context.Context, uid string) (category.Category, error) {
	c, err := scanCategory(s.db.QueryRowContext(ctx, `SELECT `+categoryCols+` FROM categories WHERE uid = ?`, uid))
	if errors.Is(err, sql.ErrNoRows) {
		return category.Category{}, ErrNotFound
	}
	return c, err
}

func countCategoryRefs(ctx context.Context, tx *sql.Tx, uid string) (int, error) {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM transactions WHERE category_uid = ?`, uid).Scan(&n)
	return n, err
}

// UpdateCategory loads category id, applies fn and stores the result in one
// database transaction. A type change is rejected with ErrCategoryInUse when
// any transaction references the category. UID/CreatedAt are preserved.
func (s *Store) UpdateCategory(ctx context.Context, id int64, fn func(cur category.Category) (category.Category, error)) (category.Category, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return category.Category{}, fmt.Errorf("update category: %w", err)
	}
	defer tx.Rollback()
	cur, err := scanCategory(tx.QueryRowContext(ctx, `SELECT `+categoryCols+` FROM categories WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return category.Category{}, ErrNotFound
	}
	if err != nil {
		return category.Category{}, err
	}
	next, err := fn(cur)
	if err != nil {
		return category.Category{}, err
	}
	if next.Type != cur.Type {
		n, err := countCategoryRefs(ctx, tx, cur.UID)
		if err != nil {
			return category.Category{}, err
		}
		if n > 0 {
			return category.Category{}, fmt.Errorf("%w (%d transaction(s)); its type cannot change", ErrCategoryInUse, n)
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	next.ID, next.UID, next.CreatedAt, next.UpdatedAt = cur.ID, cur.UID, cur.CreatedAt, &now
	if _, err := tx.ExecContext(ctx,
		`UPDATE categories SET name = ?, type = ?, description = ?, updated_at = ? WHERE id = ?`,
		next.Name, string(next.Type), next.Description, now, id); err != nil {
		if errors.Is(mapCategoryErr(err), ErrDuplicateCategory) {
			return category.Category{}, ErrDuplicateCategory
		}
		return category.Category{}, fmt.Errorf("update category: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return category.Category{}, fmt.Errorf("update category: %w", err)
	}
	return next, nil
}

// DeleteCategory removes a category. Returns ErrCategoryInUse if any
// transaction references it, ErrNotFound if it does not exist.
func (s *Store) DeleteCategory(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("delete category: %w", err)
	}
	defer tx.Rollback()
	var uid string
	if err := tx.QueryRowContext(ctx, `SELECT uid FROM categories WHERE id = ?`, id).Scan(&uid); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	n, err := countCategoryRefs(ctx, tx, uid)
	if err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("%w (%d transaction(s)); it cannot be deleted", ErrCategoryInUse, n)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM categories WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete category: %w", err)
	}
	return tx.Commit()
}

func scanCategory(r scanner) (category.Category, error) {
	var c category.Category
	var typ string
	var updated sql.NullString
	if err := r.Scan(&c.ID, &c.UID, &c.Name, &typ, &c.Description, &c.CreatedAt, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return c, err
		}
		return c, fmt.Errorf("scan category: %w", err)
	}
	c.Type = category.Type(typ)
	if updated.Valid {
		c.UpdatedAt = &updated.String
	}
	return c, nil
}
