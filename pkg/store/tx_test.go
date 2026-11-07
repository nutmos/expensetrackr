package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

// TestInTx: inTx commits on nil, rolls back and returns fn's error unchanged
// otherwise, treats errRollback as a silent rollback, and rolls back on a
// panic.
func TestInTx(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "tx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	put := func(key string) func(tx *sql.Tx) error {
		return func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO meta (key, value, updated_at) VALUES (?, '1', 'now')`, key)
			return err
		}
	}
	has := func(key string) bool {
		var n int
		if err := st.db.QueryRowContext(ctx, `SELECT count(*) FROM meta WHERE key = ?`, key).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}

	if err := st.inTx(ctx, "test", put("committed")); err != nil || !has("committed") {
		t.Errorf("commit: %v, stored %v", err, has("committed"))
	}

	err = st.inTx(ctx, "test", func(tx *sql.Tx) error {
		if err := put("failed")(tx); err != nil {
			return err
		}
		return ErrVersionConflict
	})
	if err != ErrVersionConflict || has("failed") { // unchanged, not wrapped
		t.Errorf("error: %v, stored %v", err, has("failed"))
	}

	err = st.inTx(ctx, "test", func(tx *sql.Tx) error {
		if err := put("dry-run")(tx); err != nil {
			return err
		}
		return errRollback
	})
	if err != nil || has("dry-run") {
		t.Errorf("errRollback: %v, stored %v", err, has("dry-run"))
	}

	func() {
		defer func() { _ = recover() }()
		_ = st.inTx(ctx, "test", func(tx *sql.Tx) error {
			_ = put("panicked")(tx)
			panic("boom")
		})
	}()
	if has("panicked") {
		t.Error("a panic in fn committed")
	}
	// The single connection is free again after every case.
	if err := st.inTx(ctx, "test", put("after")); err != nil || !has("after") {
		t.Errorf("after: %v", err)
	}

	// Begin failures are wrapped with op.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if err := st.inTx(cctx, "write things", put("x")); err == nil || !errors.Is(err, context.Canceled) || err.Error()[:13] != "write things:" {
		t.Errorf("cancelled begin: %v", err)
	}
}
