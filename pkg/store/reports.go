package store

import (
	"context"
	"database/sql"
	"fmt"
	"math/big"
	"strconv"
	"time"

	"github.com/nutmos/expensetrackr/pkg/common_type"
	"github.com/nutmos/expensetrackr/pkg/report"
	"github.com/nutmos/expensetrackr/pkg/transaction"
)

// SumFlows aggregates userUID's expense, income and return-on-investment
// transactions spent in [from, to] (either bound may be nil) by type,
// currency, category and stored scale, for the income & expense report.
// Sums are computed by Postgres as numeric, so they never overflow.
func (s *Store) SumFlows(ctx context.Context, userUID string, from, to *time.Time) ([]report.FlowRow, error) {
	args := []any{userUID}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	types := make([]string, len(report.FlowTypes))
	for i, t := range report.FlowTypes {
		types[i] = string(t)
	}
	query := `SELECT type, currency, category_uid, amount_scale, SUM(amount_minor)::text, COUNT(*)
		FROM transactions WHERE user_uid = $1 AND type = ANY(` + arg(types) + `)`
	if from != nil {
		query += ` AND spent_at_utc >= ` + arg(from.UTC())
	}
	if to != nil {
		query += ` AND spent_at_utc <= ` + arg(to.UTC())
	}
	query += ` GROUP BY type, currency, category_uid, amount_scale`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sum flows: %w", err)
	}
	defer rows.Close()
	out := []report.FlowRow{}
	for rows.Next() {
		var (
			r        report.FlowRow
			typ, sum string
			cat      sql.NullString
		)
		if err := rows.Scan(&typ, &r.Currency, &cat, &r.Scale, &sum, &r.Count); err != nil {
			return nil, fmt.Errorf("scan flow: %w", err)
		}
		r.Type = transaction.Type(typ)
		if cat.Valid {
			uid := common_type.UID(cat.String)
			r.CategoryUID = &uid
		}
		v, ok := new(big.Int).SetString(sum, 10)
		if !ok {
			return nil, fmt.Errorf("scan flow: bad sum %q", sum)
		}
		r.SumMinor = v
		out = append(out, r)
	}
	return out, rows.Err()
}
