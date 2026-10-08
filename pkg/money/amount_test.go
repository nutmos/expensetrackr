package money

import "testing"

func TestParseAmount(t *testing.T) {
	cases := []struct {
		in      string
		exp     int
		want    int64
		wantErr bool
	}{
		{"120", 2, 12000, false},
		{"120.5", 2, 12050, false},
		{"120.50", 2, 12050, false},
		{"0.01", 2, 1, false},
		{"007.10", 2, 710, false},
		{"1500", 0, 1500, false},  // JPY
		{"1.234", 3, 1234, false}, // KWD
		{"0.1", 2, 10, false},     // classic float trap: stays exact
		{"9999999999999.99", 2, 999999999999999, false},
		{"1500.00", 0, 1500, false}, // trailing zeros beyond the scale are harmless
		{"1.230", 2, 123, false},
		{"0.000", 2, 0, true},
		{"1.231", 2, 0, true},
		{"", 2, 0, true},
		{"0", 2, 0, true},
		{"0.00", 2, 0, true},
		{"-5", 2, 0, true},
		{"1,000", 2, 0, true},
		{"1e3", 2, 0, true},
		{"12.", 2, 0, true},
		{".5", 2, 0, true},
		{"1.234", 2, 0, true},
		{"1.5", 0, 0, true},
		{"99999999999999", 2, 0, true},
		{"฿100", 2, 0, true},
	}
	for _, c := range cases {
		got, err := ParseAmount(c.in, c.exp)
		if (err != nil) != c.wantErr {
			t.Errorf("ParseAmount(%q,%d) err=%v, wantErr=%v", c.in, c.exp, err, c.wantErr)
			continue
		}
		if !c.wantErr && got != c.want {
			t.Errorf("ParseAmount(%q,%d)=%d, want %d", c.in, c.exp, got, c.want)
		}
	}
}

func TestFormatAmount(t *testing.T) {
	cases := []struct {
		minor int64
		exp   int
		want  string
	}{
		{12050, 2, "120.50"}, {1, 2, "0.01"}, {1500, 0, "1500"}, {1234, 3, "1.234"}, {5, 4, "0.0005"},
	}
	for _, c := range cases {
		if got := FormatAmount(c.minor, c.exp); got != c.want {
			t.Errorf("FormatAmount(%d,%d)=%q, want %q", c.minor, c.exp, got, c.want)
		}
	}
}

func TestParseNonNegative(t *testing.T) {
	for in, want := range map[string]int64{"0": 0, "0.00": 0, "5000": 500000, "12.3": 1230} {
		if got, err := ParseNonNegative(in, 2); err != nil || got != want {
			t.Errorf("ParseNonNegative(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "-1", "+1", "1,000", "abc", "1.234"} {
		if _, err := ParseNonNegative(in, 2); err == nil {
			t.Errorf("ParseNonNegative(%q) expected error", in)
		}
	}
}

func TestParseSigned(t *testing.T) {
	for in, want := range map[string]int64{"0": 0, "-0": 0, "-35": -3500, "-0.01": -1, "1500.5": 150050, " -12.30 ": -1230} {
		if got, err := ParseSigned(in, 2); err != nil || got != want {
			t.Errorf("ParseSigned(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "-", "--1", "+1", "- 1", "1-", "-1.234", "-99999999999999"} {
		if _, err := ParseSigned(in, 2); err == nil {
			t.Errorf("ParseSigned(%q) expected error", in)
		}
	}
	if got := FormatAmount(-3500, 2); got != "-35.00" {
		t.Errorf("FormatAmount(-3500,2) = %q", got)
	}
}
