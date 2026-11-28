package mcp

import "testing"

func TestPathUID(t *testing.T) {
	ok := map[string]string{
		"0190a3b2-7c4d-7e8f-9a0b-1c2d3e4f5a6b":     "0190a3b2-7c4d-7e8f-9a0b-1c2d3e4f5a6b",
		"  0190A3B2-7C4D-7E8F-9A0B-1C2D3E4F5A6B\n": "0190a3b2-7c4d-7e8f-9a0b-1c2d3e4f5a6b",
	}
	for in, want := range ok {
		if got, err := pathUID("uid", in); err != nil || got != want {
			t.Errorf("pathUID(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{
		"", "   ", "not-a-uuid",
		"0190a3b27c4d7e8f9a0b1c2d3e4f5a6b",              // no dashes
		"{0190a3b2-7c4d-7e8f-9a0b-1c2d3e4f5a6b}",        // braced
		"urn:uuid:0190a3b2-7c4d-7e8f-9a0b-1c2d3e4f5a6b", // urn form
		"0190a3b2-7c4d-7e8f-9a0b-1c2d3e4f5a6g",          // non-hex
		"../0190a3b2-7c4d-7e8f-9a0b-1c2d3e4f5a",         // path traversal, 36 chars
	} {
		if got, err := pathUID("uid", in); err == nil {
			t.Errorf("pathUID(%q) = %q, want an error", in, got)
		}
	}
}
