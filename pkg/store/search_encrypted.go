package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// encryptedSearch holds a user's decrypted searchable text, for searching
// when encryption at rest is on (the database only sees ciphertext there).
// See searchCond.
type encryptedSearch struct {
	txs      []searchText // note, account and to_account of each transaction
	balances []searchText // current balance names, by uid
	cats     []searchText // category names, by uid
}

// searchText is the decrypted text of one row: an id (or uid) and its
// searchable values.
type searchText struct {
	id    int64
	uid   string
	texts []string
}

// loadEncryptedSearch reads and decrypts the searchable text of userUID's
// transactions, balances and categories.
func (s *Store) loadEncryptedSearch(ctx context.Context, userUID string) (*encryptedSearch, error) {
	enc := &encryptedSearch{}

	rows, err := s.db.QueryContext(ctx, `SELECT id, note, account, to_account FROM transactions WHERE user_uid = $1`, userUID)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var note, account, toAccount sql.NullString
		if err := rows.Scan(&id, &note, &account, &toAccount); err != nil {
			return nil, fmt.Errorf("search: %w", err)
		}
		t := searchText{id: id}
		for _, v := range []sql.NullString{note, account, toAccount} {
			if !v.Valid {
				continue
			}
			if err := s.open(&v.String, "transaction search text"); err != nil {
				return nil, err
			}
			t.texts = append(t.texts, v.String)
		}
		enc.txs = append(enc.txs, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}

	balances, err := s.ListBalances(ctx, userUID, "")
	if err != nil {
		return nil, err
	}
	for _, b := range balances {
		enc.balances = append(enc.balances, searchText{uid: string(b.UID), texts: []string{b.Name}})
	}

	cats, _, err := s.readUserCategories(ctx, s.db, userUID)
	if err != nil {
		return nil, err
	}
	for _, c := range cats {
		enc.cats = append(enc.cats, searchText{uid: string(c.UID), texts: []string{c.Name}})
	}
	return enc, nil
}

// match returns the transaction ids, balance uids and category uids whose
// text contains term, ignoring case (as ILIKE '%term%' would).
func (e *encryptedSearch) match(term string) (ids []int64, balances, cats []string) {
	term = strings.ToLower(term)
	has := func(t searchText) bool {
		for _, v := range t.texts {
			if strings.Contains(strings.ToLower(v), term) {
				return true
			}
		}
		return false
	}
	ids, balances, cats = []int64{}, []string{}, []string{}
	for _, t := range e.txs {
		if has(t) {
			ids = append(ids, t.id)
		}
	}
	for _, t := range e.balances {
		if has(t) {
			balances = append(balances, t.uid)
		}
	}
	for _, t := range e.cats {
		if has(t) {
			cats = append(cats, t.uid)
		}
	}
	return ids, balances, cats
}
