package store

import (
	"context"
	"sync"
	"testing"
)

// TestConcurrentWritesDoNotLoseUpdates: on Postgres the store runs writes
// on several connections at once (SQLite had one). Balance effects read a
// balance and write back an absolute value, so without the row locks taken
// by lockBalances some of these updates would be lost.
func TestConcurrentWritesDoNotLoseUpdates(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	const n = 40 // 40 x 1.00 THB
	var wg sync.WaitGroup
	errs := make(chan error, 3*n)
	for i := 0; i < n; i++ {
		wg.Add(3)
		go func() { // expense: pay -1.00
			defer wg.Done()
			e := f.tx("expense", "1", "THB", f.pay, nil)
			errs <- f.st.Create(ctx, f.userUID, &e)
		}()
		go func() { // transfer pay -> gold: pay -1.00, gold +1.00
			defer wg.Done()
			e := f.tx("transfer", "1", "THB", f.pay, &f.gold)
			errs <- f.st.Create(ctx, f.userUID, &e)
		}()
		go func() { // transfer gold -> pay, the opposite lock order: gold -1.00, pay +1.00
			defer wg.Done()
			e := f.tx("transfer", "1", "THB", f.gold, &f.pay)
			errs <- f.st.Create(ctx, f.userUID, &e)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent create: %v", err) // a deadlock would show up here
		}
	}
	// pay: 1000 - 40 (expenses) - 40 (to gold) + 40 (from gold) = 960.
	// gold: 500 + 40 - 40 = 500. Every write bumps each touched balance's
	// version once: pay 1 + 3n, gold 1 + 2n.
	f.want(f.pay, "960.00", 1+3*n)
	f.want(f.gold, "500.00", 1+2*n)
}
