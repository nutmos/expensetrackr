package auth

import (
	"container/list"
	"context"
	"errors"
	"math"
	"sync"
	"time"
)

// DefaultMaxKeys bounds the in-memory login throttle and rate limiter: past
// it the least recently used key is forgotten, so random usernames or
// addresses cannot grow memory without limit (issue #155).
const DefaultMaxKeys = 10000

// lru is a map with at most max entries; inserting past max drops the least
// recently used one. Get and put mark a key as used; all operations are O(1).
// Not safe for concurrent use: the owner holds a lock.
type lru[V any] struct {
	max   int
	order *list.List // front = most recently used; elements hold *lruItem[V]
	m     map[string]*list.Element
}

type lruItem[V any] struct {
	key string
	val V
}

func newLRU[V any](max int) *lru[V] {
	if max <= 0 {
		max = DefaultMaxKeys
	}
	return &lru[V]{max: max, order: list.New(), m: map[string]*list.Element{}}
}

func (l *lru[V]) get(key string) (V, bool) {
	if e, ok := l.m[key]; ok {
		l.order.MoveToFront(e)
		return e.Value.(*lruItem[V]).val, true
	}
	var zero V
	return zero, false
}

// peek is get without marking the key as used.
func (l *lru[V]) peek(key string) (V, bool) {
	if e, ok := l.m[key]; ok {
		return e.Value.(*lruItem[V]).val, true
	}
	var zero V
	return zero, false
}

func (l *lru[V]) put(key string, v V) {
	if e, ok := l.m[key]; ok {
		e.Value.(*lruItem[V]).val = v
		l.order.MoveToFront(e)
		return
	}
	for len(l.m) >= l.max {
		oldest := l.order.Back()
		l.order.Remove(oldest)
		delete(l.m, oldest.Value.(*lruItem[V]).key)
	}
	l.m[key] = l.order.PushFront(&lruItem[V]{key: key, val: v})
}

func (l *lru[V]) remove(key string) {
	if e, ok := l.m[key]; ok {
		l.order.Remove(e)
		delete(l.m, key)
	}
}

func (l *lru[V]) len() int { return len(l.m) }

// RateLimiter is a token bucket per key (e.g. a client IP): each key may do
// Burst requests at once, refilled at one per Every. State is in memory and
// bounded to MaxKeys keys (least recently used forgotten first).
type RateLimiter struct {
	Burst   int
	Every   time.Duration
	MaxKeys int // 0 = DefaultMaxKeys
	Now     func() time.Time

	mu      sync.Mutex
	buckets *lru[*bucket]
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewLoginRateLimiter returns the default limit on password endpoints per
// client IP: 10 at once, then one every 6 seconds (10 a minute).
func NewLoginRateLimiter() *RateLimiter {
	return &RateLimiter{Burst: 10, Every: 6 * time.Second, Now: time.Now}
}

// Allow takes one token for key. When none is left it returns false and how
// long until the next one.
func (r *RateLimiter) Allow(key string) (bool, time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.buckets == nil {
		r.buckets = newLRU[*bucket](r.MaxKeys)
	}
	now := r.Now()
	b, ok := r.buckets.get(key)
	if !ok {
		b = &bucket{tokens: float64(r.Burst), last: now}
		r.buckets.put(key, b)
	}
	if el := now.Sub(b.last); el > 0 {
		b.tokens = math.Min(float64(r.Burst), b.tokens+float64(el)/float64(r.Every))
	}
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, time.Duration(math.Ceil((1 - b.tokens) * float64(r.Every)))
}

// Gate caps how many expensive operations (bcrypt hashes and comparisons)
// run at once, so a flood of password requests cannot take every CPU.
type Gate struct{ slots chan struct{} }

// ErrBusy is returned by Gate.Acquire when no slot frees up in time.
var ErrBusy = errors.New("server busy")

// NewGate allows n concurrent holders (at least 1).
func NewGate(n int) *Gate {
	if n < 1 {
		n = 1
	}
	return &Gate{slots: make(chan struct{}, n)}
}

// Acquire waits up to wait (or until ctx ends) for a slot. On success the
// caller must Release it.
func (g *Gate) Acquire(ctx context.Context, wait time.Duration) error {
	select {
	case g.slots <- struct{}{}:
		return nil
	default:
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case g.slots <- struct{}{}:
		return nil
	case <-t.C:
		return ErrBusy
	case <-ctx.Done():
		return ErrBusy
	}
}

// Release frees a slot taken by Acquire.
func (g *Gate) Release() { <-g.slots }
