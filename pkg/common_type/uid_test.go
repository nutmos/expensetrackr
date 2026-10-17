package common_type

import "testing"

func TestNormalizeUID(t *testing.T) {
	if got := NormalizeUID("  A1B2C3D4-E5F6-4789-A012-3456789ABCDE "); got != "a1b2c3d4-e5f6-4789-a012-3456789abcde" {
		t.Errorf("NormalizeUID = %q", got)
	}
}

func TestUIDValid(t *testing.T) {
	for _, s := range []UID{"a1b2c3d4-e5f6-4789-a012-3456789abcde", "A1B2C3D4-E5F6-4789-A012-3456789ABCDE"} {
		if !s.Valid() {
			t.Errorf("%q: want valid", s)
		}
	}
	for _, s := range []UID{"", "not-a-uuid", "a1b2c3d4e5f64789a0123456789abcde", "a1b2c3d4-e5f6-4789-a012-3456789abcdg", "a1b2c3d4-e5f6-4789-a012_3456789abcde"} {
		if s.Valid() {
			t.Errorf("%q: want invalid", s)
		}
	}
}

func TestUIDMatches(t *testing.T) {
	if !UID("a1b2c3d4-e5f6-4789-a012-3456789abcde").Matches("A1B2C3D4-E5F6-4789-A012-3456789ABCDE") {
		t.Error("Matches should ignore case")
	}
	if UID("a").Matches("b") {
		t.Error("Matches(a, b)")
	}
}
