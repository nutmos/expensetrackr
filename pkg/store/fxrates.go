package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// FXLookup returns the cached rate for (base, quote) at the requested date
// ("latest" or YYYY-MM-DD): the rate text, the date it applies to, and when
// the lookup was fetched. ok is false on a cache miss.
func (s *Store) FXLookup(ctx context.Context, base, quote, requested string) (rate, rateDate string, fetchedAt time.Time, ok bool, err error) {
	var fetched string
	err = s.db.QueryRowContext(ctx, `
		SELECT r.rate, r.rate_date, l.fetched_at FROM fx_rate_lookups l
		JOIN fx_rates r ON r.base = l.base AND r.quote = l.quote AND r.rate_date = l.rate_date
		WHERE l.base = ? AND l.quote = ? AND l.requested_date = ?`, base, quote, requested).Scan(&rate, &rateDate, &fetched)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", time.Time{}, false, nil
	}
	if err != nil {
		return "", "", time.Time{}, false, fmt.Errorf("read fx cache: %w", err)
	}
	fetchedAt, _ = time.Parse(time.RFC3339, fetched)
	return rate, rateDate, fetchedAt, true, nil
}

// FXStore caches a fetched rate and the requested -> rate date mapping.
func (s *Store) FXStore(ctx context.Context, base, quote, requested, rateDate, rate string, fetchedAt time.Time) error {
	at := fetchedAt.UTC().Format(time.RFC3339)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("write fx cache: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO fx_rates (base, quote, rate_date, rate, fetched_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (base, quote, rate_date) DO UPDATE SET rate = excluded.rate, fetched_at = excluded.fetched_at`,
		base, quote, rateDate, rate, at); err != nil {
		return fmt.Errorf("write fx cache: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO fx_rate_lookups (base, quote, requested_date, rate_date, fetched_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (base, quote, requested_date) DO UPDATE SET rate_date = excluded.rate_date, fetched_at = excluded.fetched_at`,
		base, quote, requested, rateDate, at); err != nil {
		return fmt.Errorf("write fx cache: %w", err)
	}
	return tx.Commit()
}
