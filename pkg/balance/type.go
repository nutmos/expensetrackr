package balance

import (
	"strings"

	"github.com/nutmos/expensetrackr/pkg/helper"
)

// Type is the kind of balance.
type Type string

const (
	TypeUnknown        Type = "unknown"
	TypePaymentAccount Type = "payment_account"
	TypeCreditCard     Type = "credit_card"
	TypeOtherAsset     Type = "other_asset"
	TypeOtherLiability Type = "other_liability"
)

// Types lists every type in display order.
var Types = []Type{TypePaymentAccount, TypeCreditCard, TypeOtherAsset, TypeOtherLiability}

// Kind says whether a type is an asset (it has a balance) or a liability (it
// has a debt and a limit).
type Kind string

const (
	KindUnknown   Kind = "unknown"
	KindAsset     Kind = "asset"
	KindLiability Kind = "liability"
)

// ParseType trims and lower-cases a type as sent by a client. The result is
// not checked; use Valid.
func ParseType(s string) Type { return Type(helper.Normalize(s)) }

// Valid reports whether t is a known type.
func (t Type) Valid() bool { return t.Kind() != KindUnknown }

// Kind is KindAsset or KindLiability, or KindUnknown ("unknown") for an
// unknown type.
func (t Type) Kind() Kind {
	if t == TypePaymentAccount || t == TypeOtherAsset {
		return KindAsset
	} else if t == TypeCreditCard || t == TypeOtherLiability {
		return KindLiability
	}
	return KindUnknown
}

// IsAsset reports whether t carries a balance (payment_account, other_asset).
func (t Type) IsAsset() bool { return t.Kind() == KindAsset }

// IsLiability reports whether t carries a debt and a limit (credit_card, other_liability).
func (t Type) IsLiability() bool { return t.Kind() == KindLiability }

// IsPayable reports whether a balance of type t can pay for a transaction
// (payment_account and credit_card).
func (t Type) IsPayable() bool { return t == TypePaymentAccount || t == TypeCreditCard }

// TypeList is every type in display order, comma-separated, for messages.
func TypeList() string {
	s := make([]string, len(Types))
	for i, t := range Types {
		s[i] = string(t)
	}
	return strings.Join(s, ", ")
}
