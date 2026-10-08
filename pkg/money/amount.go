// Package money handles currency codes and exact decimal amounts stored as
// integer minor units (no floating point).
package money

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// MaxIntegerDigits bounds the integer part of an amount so that the value in
// minor units always fits comfortably in an int64 (even with 4 decimals).
const MaxIntegerDigits = 13

// Error hints for the different parse modes.
const (
	hintPositive    = "must be a positive plain decimal number like 120 or 120.50 (no signs, commas or symbols)"
	hintNonNegative = "must be a plain decimal number of 0 or more, like 0, 120 or 120.50 (no signs, commas or symbols)"
	hintSigned      = "must be a plain decimal number like 120.50 or -35 (no commas or symbols)"
)

// ParseAmount converts a plain decimal string such as "123.45" into integer
// minor units for a currency with the given exponent (e.g. 12345 for 2).
// No floating point is involved at any stage. The amount must be positive.
func ParseAmount(s string, exponent int) (int64, error) {
	v, err := parseUnsigned(s, exponent, hintPositive)
	if err == nil && v == 0 {
		return 0, errors.New("must be greater than zero")
	}
	return v, err
}

// ParseNonNegative is like ParseAmount but also accepts zero (e.g. a debt of 0).
func ParseNonNegative(s string, exponent int) (int64, error) {
	return parseUnsigned(s, exponent, hintNonNegative)
}

// ParseSigned is like ParseNonNegative but also accepts a leading minus sign
// (e.g. an overdrawn account balance of "-35.00").
func ParseSigned(s string, exponent int) (int64, error) {
	s = strings.TrimSpace(s)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
		if s == "" || s[0] < '0' || s[0] > '9' { // reject "-", "--1", "- 1"
			return 0, errors.New(hintSigned)
		}
	}
	v, err := parseUnsigned(s, exponent, hintSigned)
	if neg {
		v = -v
	}
	return v, err
}

// parseUnsigned does the exact decimal -> minor units conversion shared by
// the Parse* functions. Zero is allowed here; callers decide.
func parseUnsigned(s string, exponent int, hint string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("is required")
	}
	intPart, fracPart, hasDot := strings.Cut(s, ".")
	if intPart == "" || (hasDot && fracPart == "") {
		return 0, errors.New("must be a plain decimal number like 120 or 120.50")
	}
	for _, part := range []string{intPart, fracPart} {
		for _, r := range part {
			if r < '0' || r > '9' {
				return 0, errors.New(hint)
			}
		}
	}
	if len(fracPart) > exponent {
		// Extra trailing zeros carry no value ("1500.00" JPY == 1500), so drop
		// them before deciding whether the amount needs rounding.
		fracPart = strings.TrimRight(fracPart, "0")
		if len(fracPart) < exponent {
			fracPart += strings.Repeat("0", exponent-len(fracPart))
		}
	}
	if len(fracPart) > exponent {
		if exponent == 0 {
			return 0, errors.New("this currency has no minor unit; use a whole number")
		}
		return 0, fmt.Errorf("has too many decimal places (max %d for this currency)", exponent)
	}
	intPart = strings.TrimLeft(intPart, "0")
	if len(intPart) > MaxIntegerDigits {
		return 0, fmt.Errorf("is too large (max %d digits before the decimal point)", MaxIntegerDigits)
	}
	digits := intPart + fracPart + strings.Repeat("0", exponent-len(fracPart))
	digits = strings.TrimLeft(digits, "0")
	if digits == "" {
		return 0, nil
	}
	v, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0, errors.New("is out of range")
	}
	return v, nil
}

// FormatAmount renders integer minor units back into a decimal string,
// e.g. FormatAmount(12345, 2) == "123.45".
func FormatAmount(minor int64, exponent int) string {
	neg := minor < 0
	if neg {
		minor = -minor
	}
	s := strconv.FormatInt(minor, 10)
	if exponent > 0 {
		if len(s) <= exponent {
			s = strings.Repeat("0", exponent-len(s)+1) + s
		}
		s = s[:len(s)-exponent] + "." + s[len(s)-exponent:]
	}
	if neg {
		s = "-" + s
	}
	return s
}
