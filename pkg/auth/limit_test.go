package auth

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestLRU(t *testing.T) {
	l := newLRU[int](3)
	l.put("a", 1)
	l.put("b", 2)
	l.put("c", 3)
	l.get("a")    // a is now the most recently used; b the least
	l.put("d", 4) // evicts b
	if _, ok := l.peek("b"); ok || l.len() != 3 {
		t.Fatalf("b not evicted: len %d", l.len())
	}
	for k, want := range map[string]int{"a": 1, "c": 3, "d": 4} {
		if v, ok := l.peek(k); !ok || v != want {
			t.Errorf("%s = %d, %v", k, v, ok)
		}
	}
	l.put("c", 30) // update in place, no eviction
	if v, _ := l.peek("c"); v != 30 || l.len() != 3 {
		t.Errorf("update: c=%d len=%d", v, l.len())
	}
	l.remove("c")
	l.remove("zzz")
	if _, ok := l.peek("c"); ok || l.len() != 2 {
		t.Errorf("remove: len %d", l.len())
	}
}

// Random usernames cannot grow the throttle past MaxKeys (issue #155).
func TestThrottleBounded(t *testing.T) {
	now := time.Unix(1000, 0)
	th := &Throttle{MaxFailures: 5, Window: 15 * time.Minute, Lockout: 5 * time.Minute, MaxKeys: 100, Now: func() time.Time { return now }}
	for i := 0; i < 5; i++ {
		th.Fail("victim")
	}
	for i := 0; i < 50; i++ {
		th.Fail(fmt.Sprintf("random-%d", i))
	}
	if th.Locked("victim") == 0 {
		t.Fatal("lock lost below the cap")
	}
	for i := 50; i < 10000; i++ {
		th.Fail(fmt.Sprintf("random-%d", i))
	}
	if n := th.m.len(); n != 100 {
		t.Errorf("throttle holds %d keys, want 100", n)
	}
}

// A lock survives the failure window restarting.
func TestThrottleKeepsLockAcrossWindow(t *testing.T) {
	now := time.Unix(1000, 0)
	th := &Throttle{MaxFailures: 2, Window: time.Minute, Lockout: 10 * time.Minute, Now: func() time.Time { return now }}
	th.Fail("k")
	th.Fail("k") // locked until +10m, window restarts now
	now = now.Add(2 * time.Minute)
	th.Fail("k") // new window
	if d := th.Locked("k"); d != 8*time.Minute {
		t.Errorf("locked %v, want 8m", d)
	}
}

func TestRateLimiter(t *testing.T) {
	now := time.Unix(1000, 0)
	r := &RateLimiter{Burst: 3, Every: 10 * time.Second, MaxKeys: 2, Now: func() time.Time { return now }}
	for i := 0; i < 3; i++ {
		if ok, _ := r.Allow("ip1"); !ok {
			t.Fatalf("request %d refused", i)
		}
	}
	if ok, wait := r.Allow("ip1"); ok || wait != 10*time.Second {
		t.Fatalf("4th: ok=%v wait=%v", ok, wait)
	}
	if ok, _ := r.Allow("ip2"); !ok {
		t.Fatal("other key limited")
	}
	now = now.Add(4 * time.Second)
	if ok, wait := r.Allow("ip1"); ok || wait != 6*time.Second {
		t.Fatalf("after 4s: ok=%v wait=%v", ok, wait)
	}
	now = now.Add(6 * time.Second)
	if ok, _ := r.Allow("ip1"); !ok {
		t.Fatal("not refilled after 10s")
	}
	// Refill is capped at Burst.
	now = now.Add(time.Hour)
	for i := 0; i < 3; i++ {
		r.Allow("ip1")
	}
	if ok, _ := r.Allow("ip1"); ok {
		t.Error("burst exceeded after a long idle")
	}
	// Bounded: a third key evicts the least recently used.
	r.Allow("ip3")
	if n := r.buckets.len(); n != 2 {
		t.Errorf("limiter holds %d keys, want 2", n)
	}
}

func TestGate(t *testing.T) {
	g := NewGate(2)
	ctx := context.Background()
	if g.Acquire(ctx, 0) != nil || g.Acquire(ctx, 0) != nil {
		t.Fatal("free slots refused")
	}
	start := time.Now()
	if err := g.Acquire(ctx, 20*time.Millisecond); err != ErrBusy || time.Since(start) < 20*time.Millisecond {
		t.Fatalf("full gate: %v after %v", err, time.Since(start))
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if err := g.Acquire(cctx, time.Minute); err != ErrBusy {
		t.Fatalf("cancelled: %v", err)
	}
	go func() { time.Sleep(10 * time.Millisecond); g.Release() }()
	if err := g.Acquire(ctx, time.Second); err != nil {
		t.Fatalf("waiting for a released slot: %v", err)
	}
	if NewGate(0).Acquire(ctx, 0) != nil {
		t.Error("NewGate(0) has no slot")
	}
}
