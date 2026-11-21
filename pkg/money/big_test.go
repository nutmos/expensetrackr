package money

import (
	"math/big"
	"testing"
)

func TestFormatBigAmount(t *testing.T) {
	huge, _ := new(big.Int).SetString("-18446744073709551614", 10)
	for _, c := range []struct {
		v    *big.Int
		exp  int
		want string
	}{
		{big.NewInt(12345), 2, "123.45"},
		{big.NewInt(-5), 2, "-0.05"},
		{big.NewInt(1500), 0, "1500"},
		{huge, 2, "-184467440737095516.14"},
		{huge, 0, "-18446744073709551614"},
		{new(big.Int).Neg(huge), 25, "0.0000018446744073709551614"},
	} {
		if got := FormatBigAmount(c.v, c.exp); got != c.want {
			t.Errorf("FormatBigAmount(%s, %d) = %s, want %s", c.v, c.exp, got, c.want)
		}
	}
}

func TestRescaleMinor(t *testing.T) {
	if got := RescaleMinor(big.NewInt(150), 2, 3); got.Int64() != 1500 {
		t.Errorf("up: %s", got)
	}
	if got := RescaleMinor(big.NewInt(1509), 3, 2); got.Int64() != 150 {
		t.Errorf("down: %s", got)
	}
	v := big.NewInt(7)
	if got := RescaleMinor(v, 2, 2); got.Int64() != 7 || got == v {
		t.Errorf("same scale must copy: %s", got)
	}
}
