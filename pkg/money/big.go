package money

import (
	"math/big"
	"strings"
)

// FormatBigAmount is FormatAmount for an arbitrary-precision amount: minor
// units with exponent decimal places, e.g. 12345 at 2 -> "123.45". Sums of
// many int64 amounts (totals, reports) use it so they never overflow.
func FormatBigAmount(minor *big.Int, exponent int) string {
	if minor.IsInt64() {
		return FormatAmount(minor.Int64(), exponent)
	}
	digits := new(big.Int).Abs(minor).String()
	if exponent > 0 {
		if len(digits) <= exponent {
			digits = strings.Repeat("0", exponent-len(digits)+1) + digits
		}
		digits = digits[:len(digits)-exponent] + "." + digits[len(digits)-exponent:]
	}
	if minor.Sign() < 0 {
		digits = "-" + digits
	}
	return digits
}

// RescaleMinor converts minor units stored with exponent from to exponent to
// (e.g. 1200 at 2 -> 12000 at 3). Going to fewer digits truncates toward
// zero; stored amounts only ever gain digits when a currency table changes.
func RescaleMinor(v *big.Int, from, to int) *big.Int {
	out := new(big.Int).Set(v)
	if to > from {
		out.Mul(out, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(to-from)), nil))
	} else if to < from {
		out.Quo(out, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(from-to)), nil))
	}
	return out
}
