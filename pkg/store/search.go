package store

import (
	"context"
	"fmt"
	"math/big"
	"strings"

	"github.com/nutmos/expensetrackr/pkg/money"
	"github.com/nutmos/expensetrackr/pkg/transaction"
)

// Search with encryption at rest.
//
// searchCond matches the words in SQL, which only works on plain text: with
// a keyring the note, the balance names and the categories hold ciphertext
// (encryption.go). Then List runs the other filters in SQL and matches the
// words here, on the decrypted rows, with the same rules as searchCond.

// searchNames are the user's current balance and category names (decrypted),
// by uid, for matching search words.
type searchNames struct {
	balances   map[string]string
	categories map[string]string
}

// loadSearchNames reads and decrypts the names of the user's balances and
// categories.
func (s *Store) loadSearchNames(ctx context.Context, userUID string) (searchNames, error) {
	n := searchNames{balances: map[string]string{}, categories: map[string]string{}}
	rows, err := s.db.QueryContext(ctx, `SELECT uid, name FROM balances WHERE user_uid = $1`, userUID)
	if err != nil {
		return n, fmt.Errorf("list transactions: balance names: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var uid, name string
		if err := rows.Scan(&uid, &name); err != nil {
			return n, fmt.Errorf("list transactions: balance names: %w", err)
		}
		if err := s.open(&name, "balance name"); err != nil {
			return n, err
		}
		n.balances[uid] = name
	}
	if err := rows.Err(); err != nil {
		return n, fmt.Errorf("list transactions: balance names: %w", err)
	}
	cats, _, err := s.readUserCategories(ctx, s.db, userUID)
	if err != nil {
		return n, err
	}
	for _, c := range cats {
		n.categories[string(c.UID)] = c.Name
	}
	return n, nil
}

// matchSearch reports whether e (decrypted) matches every search word, as
// searchCond would on plain text.
func matchSearch(e transaction.Transaction, terms []string, names searchNames) bool {
	for _, t := range terms {
		if !matchTerm(e, t, names) {
			return false
		}
	}
	return true
}

func matchTerm(e transaction.Transaction, term string, names searchNames) bool {
	word := strings.ToLower(term)
	has := func(v string) bool { return strings.Contains(strings.ToLower(v), word) }
	hasPtr := func(v *string) bool { return v != nil && has(*v) }
	if has(e.Note) || has(e.Account) || hasPtr(e.ToAccount) || hasPtr(e.ImportRef) ||
		has(strings.ReplaceAll(string(e.Type), "_", " ")) ||
		has(e.Currency) || hasPtr(e.ToCurrency) || hasPtr(e.ForeignCurrency) {
		return true
	}
	if name, ok := names.balances[string(e.BalanceUID)]; ok && has(name) {
		return true
	}
	if e.ToBalanceUID != nil {
		if name, ok := names.balances[string(*e.ToBalanceUID)]; ok && has(name) {
			return true
		}
	}
	if e.CategoryUID != nil {
		if name, ok := names.categories[string(*e.CategoryUID)]; ok && has(name) {
			return true
		}
	}
	if n, ok := searchNumber(term); ok {
		want, ok := new(big.Rat).SetString(n)
		if !ok {
			return false
		}
		equal := func(minor *int64, currency *string) bool {
			if minor == nil || currency == nil {
				return false
			}
			scale, ok := money.MinorUnits(*currency)
			if !ok {
				return false
			}
			v := new(big.Rat).SetFrac(big.NewInt(*minor), new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil))
			return v.Cmp(want) == 0
		}
		if equal(&e.AmountMinor, &e.Currency) || equal(e.ToAmountMinor, e.ToCurrency) || equal(e.ForeignAmountMinor, e.ForeignCurrency) {
			return true
		}
	}
	return false
}
