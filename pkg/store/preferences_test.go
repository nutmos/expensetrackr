package store

import (
	"context"
	"testing"

	"github.com/nutmos/expensetrackr/pkg/user"
)

// TestUserPreferencesRoundTrip checks what reaches the users.preferences
// column and what comes back, including rows written by other versions.
func TestUserPreferencesRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	u := user.User{DisplayName: "Prefs"}
	if err := st.CreateUser(ctx, &u); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		write      func() error
		wantColumn string // "" = don't check
		want       user.Preferences
	}{
		{"new user stores {}", func() error { return nil }, `{}`, user.Preferences{}},
		{"set via UpdateUser", func() error {
			_, err := st.UpdateUser(ctx, u.ID, func(cur user.User) (user.User, error) {
				cur.Preferences = user.Preferences{BaseCurrency: "THB"}
				return cur, nil
			})
			return err
		}, `{"base_currency":"THB"}`, user.Preferences{BaseCurrency: "THB"}},
		{"unknown keys in the row are tolerated", func() error {
			_, err := st.db.ExecContext(ctx, `UPDATE users SET preferences = '{"theme":"dark","base_currency":"EUR"}' WHERE id = $1`, u.ID)
			return err
		}, "", user.Preferences{BaseCurrency: "EUR"}},
		{"missing keys are tolerated", func() error {
			_, err := st.db.ExecContext(ctx, `UPDATE users SET preferences = '{"theme":"dark"}' WHERE id = $1`, u.ID)
			return err
		}, "", user.Preferences{}},
		{"reset stores {}", func() error {
			_, err := st.UpdateUser(ctx, u.ID, func(cur user.User) (user.User, error) {
				cur.Preferences = user.Preferences{}
				return cur, nil
			})
			return err
		}, `{}`, user.Preferences{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.write(); err != nil {
				t.Fatal(err)
			}
			if tc.wantColumn != "" {
				// jsonb normalizes spacing and key order: compare as JSON.
				var col string
				var same bool
				if err := st.db.QueryRowContext(ctx, `SELECT preferences::text, preferences = $2::jsonb FROM users WHERE id = $1`, u.ID, tc.wantColumn).Scan(&col, &same); err != nil || !same {
					t.Errorf("column %s %v, want %s", col, err, tc.wantColumn)
				}
			}
			got, err := st.GetUserByUID(ctx, u.UID)
			if err != nil || got.Preferences != tc.want {
				t.Errorf("read %+v %v, want %+v", got.Preferences, err, tc.want)
			}
		})
	}
}
