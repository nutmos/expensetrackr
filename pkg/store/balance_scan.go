package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/nutmos/expensetrackr/pkg/balance"
)

const balanceCols = `id, uid, name, type, currency, description, amount_scale, balance_minor, debt_minor, limit_minor, hidden, created_at, updated_at, version`

// nullInt is *p, or SQL NULL for nil.
func nullInt(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

// scanBalances reads every row of balanceCols and closes rows.
func scanBalances(rows *sql.Rows) ([]balance.Balance, error) {
	defer rows.Close()
	out := []balance.Balance{}
	for rows.Next() {
		b, err := scanBalance(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func scanBalance(r scanner) (balance.Balance, error) {
	var b balance.Balance
	var typ string
	var scale int
	var bal, debt, limit sql.NullInt64
	var created time.Time
	var updated sql.NullTime
	if err := r.Scan(&b.ID, &b.UID, &b.Name, &typ, &b.Currency, &b.Description, &scale,
		&bal, &debt, &limit, &b.Hidden, &created, &updated, &b.Version); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return b, err
		}
		return b, fmt.Errorf("scan balance: %w", err)
	}
	b.Type = balance.Type(typ)
	ptr := func(n sql.NullInt64) *int64 {
		if !n.Valid {
			return nil
		}
		v := n.Int64
		return &v
	}
	b.SetAmounts(scale, ptr(bal), ptr(debt), ptr(limit))
	b.CreatedAt, b.UpdatedAt = ts(created), nullTS(updated)
	return b, nil
}
