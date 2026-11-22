// Package auth holds the building blocks of username/password sign-in:
// password hashing (bcrypt), password rules, session tokens and a small
// in-memory failed-login throttle. HTTP wiring lives in pkg/api.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	// MinPasswordLen is the minimum password length in bytes.
	MinPasswordLen = 8
	// MaxPasswordLen is bcrypt's input limit; longer passwords are rejected
	// rather than silently truncated.
	MaxPasswordLen = 72
	// SessionTTL is how long a session lives without being used (sliding).
	SessionTTL = 30 * 24 * time.Hour
	// TokenBytes is the size of the random session token.
	TokenBytes = 32
)

// BcryptCost is the bcrypt work factor. Tests may lower it.
var BcryptCost = 12

// CheckPasswordRules returns "" if pw is acceptable, else a message.
func CheckPasswordRules(pw string) string {
	if len(pw) < MinPasswordLen {
		return "must be at least 8 characters"
	}
	if len(pw) > MaxPasswordLen {
		return "must be at most 72 bytes"
	}
	return ""
}

// HashPassword returns a bcrypt hash ("$2a$12$...") of pw.
func HashPassword(pw string) (string, error) {
	if len(pw) > MaxPasswordLen {
		return "", errors.New("password too long")
	}
	b, err := bcrypt.GenerateFromPassword([]byte(pw), BcryptCost)
	return string(b), err
}

// dummyHash is compared against when the username does not exist, so a
// failed login takes about as long whether or not the user exists.
var (
	dummyOnce sync.Once
	dummyHash []byte
)

// CheckPassword reports whether pw matches hash. An empty hash (unknown user
// or a user without a password) still runs one bcrypt comparison and fails.
func CheckPassword(hash, pw string) bool {
	if hash == "" {
		dummyOnce.Do(func() { dummyHash, _ = bcrypt.GenerateFromPassword([]byte("dummy-password"), BcryptCost) })
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(pw))
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// NewToken returns a random URL-safe session token (32 bytes of entropy).
func NewToken() (string, error) {
	b := make([]byte, TokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashToken is what the database stores for a token (SHA-256, hex). The raw
// token only ever lives in the client's cookie.
func HashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

// Throttle counts failed logins per key (username + client IP). After
// MaxFailures failures within Window the key is locked for Lockout. State is
// in memory only (reset on restart) and holds at most MaxKeys keys: past
// that the least recently failed key is forgotten, so random usernames
// cannot grow memory (issue #155; the per-IP RateLimiter keeps one client
// from cycling keys fast enough to push out a lock).
type Throttle struct {
	MaxFailures int
	Window      time.Duration
	Lockout     time.Duration
	MaxKeys     int // 0 = DefaultMaxKeys
	Now         func() time.Time

	mu sync.Mutex
	m  *lru[*throttleEntry]
}

type throttleEntry struct {
	failures    int
	first       time.Time
	lockedUntil time.Time
}

// NewThrottle returns the default throttle: 5 failures in 15 minutes lock
// the key for 5 minutes.
func NewThrottle() *Throttle {
	return &Throttle{MaxFailures: 5, Window: 15 * time.Minute, Lockout: 5 * time.Minute, Now: time.Now}
}

// Locked returns how long key is still locked (0 if not).
func (t *Throttle) Locked(key string) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.m == nil {
		return 0
	}
	e, ok := t.m.peek(key)
	if !ok {
		return 0
	}
	if d := e.lockedUntil.Sub(t.Now()); d > 0 {
		return d
	}
	return 0
}

// Fail records a failure for key.
func (t *Throttle) Fail(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.Now()
	if t.m == nil {
		t.m = newLRU[*throttleEntry](t.MaxKeys)
	}
	e, ok := t.m.get(key)
	if !ok || now.Sub(e.first) > t.Window {
		e = &throttleEntry{first: now, lockedUntil: lockedUntil(e)}
		t.m.put(key, e)
	}
	e.failures++
	if e.failures >= t.MaxFailures {
		e.lockedUntil = now.Add(t.Lockout)
		e.failures = 0
		e.first = now
	}
}

// lockedUntil keeps a running lock when a key's failure window restarts.
func lockedUntil(e *throttleEntry) time.Time {
	if e == nil {
		return time.Time{}
	}
	return e.lockedUntil
}

// Reset forgets key (after a successful login).
func (t *Throttle) Reset(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.m != nil {
		t.m.remove(key)
	}
}

// Personal API tokens (docs/auth.md, "API tokens").
const (
	// APITokenPrefix starts every API token so it is recognizable in logs,
	// config files and secret scanners.
	APITokenPrefix = "ext_"
	// APITokenDisplayLen is how many leading characters of a token (prefix
	// included) are kept as a non-secret hint for lists.
	APITokenDisplayLen = len(APITokenPrefix) + 8
)

// NewAPIToken returns a new API token (APITokenPrefix + 32 random bytes,
// base64url) and its non-secret display prefix.
func NewAPIToken() (token, display string, err error) {
	raw, err := NewToken()
	if err != nil {
		return "", "", err
	}
	token = APITokenPrefix + raw
	return token, token[:APITokenDisplayLen], nil
}
