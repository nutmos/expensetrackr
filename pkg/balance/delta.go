package balance

import (
	"errors"
	"fmt"
	"math"
)

// ErrOverflow is returned by ApplyValueDelta when the result does not fit.
var ErrOverflow = errors.New("amount out of range")

// ApplyValueDelta changes the balance's value by delta minor units (same
// currency and scale as the balance). Value is the balance of an asset and
// minus the debt of a liability, so a positive delta raises an asset's
// balance or lowers a liability's debt (possibly below zero: in credit). The
// derived fields (available, over_limit) are recomputed.
//
// It returns ErrOverflow, leaving b unchanged, when the new balance or debt,
// or a liability's available amount (limit - debt), would not fit in int64.
func (b *Balance) ApplyValueDelta(delta int64) error {
	if b.Type.IsAsset() && b.BalanceMinor != nil {
		return b.applyAssetDelta(delta)
	} else if b.Type.IsLiability() && b.DebtMinor != nil && b.LimitMinor != nil {
		return b.applyLiabilityDelta(delta)
	}
	return fmt.Errorf("balance %q has no amount for type %q", b.UID, b.Type)
}

// applyAssetDelta adds delta to the balance.
func (b *Balance) applyAssetDelta(delta int64) error {
	v, ok := addChecked(*b.BalanceMinor, delta)
	if !ok {
		return ErrOverflow
	}
	b.SetAmounts(b.Scale, &v, nil, nil)
	return nil
}

// applyLiabilityDelta subtracts delta from the debt; limit - debt must still fit.
func (b *Balance) applyLiabilityDelta(delta int64) error {
	if delta == math.MinInt64 { // -delta would overflow
		return ErrOverflow
	}
	v, ok := addChecked(*b.DebtMinor, -delta)
	if !ok {
		return ErrOverflow
	}
	limit := *b.LimitMinor
	if _, ok := subChecked(limit, v); !ok {
		return ErrOverflow
	}
	b.SetAmounts(b.Scale, nil, &v, &limit)
	return nil
}

// addChecked returns a + b and whether it fits in int64.
func addChecked(a, b int64) (int64, bool) {
	c := a + b
	if (b > 0 && c < a) || (b < 0 && c > a) {
		return 0, false
	}
	return c, true
}

// subChecked returns a - b and whether it fits in int64.
func subChecked(a, b int64) (int64, bool) {
	c := a - b
	if (b > 0 && c > a) || (b < 0 && c < a) {
		return 0, false
	}
	return c, true
}
