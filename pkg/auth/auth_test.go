package auth

import (
	"strings"
	"testing"
	"time"
)

func init() { BcryptCost = 4 }

func TestHashAndCheck(t *testing.T) {
	h, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$2a$") || strings.Contains(h, "correct horse") {
		t.Fatalf("unexpected hash %q", h)
	}
	if !CheckPassword(h, "correct horse") {
		t.Error("right password rejected")
	}
	if CheckPassword(h, "wrong horse") || CheckPassword("", "correct horse") {
		t.Error("wrong password accepted")
	}
	h2, _ := HashPassword("correct horse")
	if h == h2 {
		t.Error("hashes are not salted")
	}
	if _, err := HashPassword(strings.Repeat("a", 73)); err == nil {
		t.Error("73-byte password hashed")
	}
}

func TestPasswordRules(t *testing.T) {
	for pw, ok := range map[string]bool{"": false, "1234567": false, "12345678": true, strings.Repeat("x", 72): true, strings.Repeat("x", 73): false} {
		if got := CheckPasswordRules(pw) == ""; got != ok {
			t.Errorf("%d bytes: ok=%v want %v", len(pw), got, ok)
		}
	}
}

func TestToken(t *testing.T) {
	a, _ := NewToken()
	b, _ := NewToken()
	if a == b || len(a) != 43 {
		t.Fatalf("tokens %q %q", a, b)
	}
	if HashToken(a) == a || len(HashToken(a)) != 64 || HashToken(a) != HashToken(a) {
		t.Error("bad token hash")
	}
}

func TestThrottle(t *testing.T) {
	now := time.Unix(1000, 0)
	th := NewThrottle()
	th.Now = func() time.Time { return now }
	for i := 0; i < 4; i++ {
		th.Fail("k")
	}
	if th.Locked("k") != 0 {
		t.Fatal("locked after 4 failures")
	}
	th.Fail("k")
	if th.Locked("k") != 5*time.Minute || th.Locked("other") != 0 {
		t.Fatal("not locked after 5 failures")
	}
	now = now.Add(5*time.Minute + time.Second)
	if th.Locked("k") != 0 {
		t.Fatal("still locked after lockout")
	}
	th.Fail("j")
	th.Reset("j")
	if th.m["j"] != nil {
		t.Error("reset kept entry")
	}
}
