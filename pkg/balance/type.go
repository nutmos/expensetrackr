package balance

import "strings"

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

// Valid reports whether t is a known type.
func (t Type) Valid() bool { return t.IsAsset() || t.IsLiability() }

type Kind string

const (
	KindUnknown   Kind = "unknown"
	KindAsset     Kind = "asset"
	KindLiability Kind = "liability"
)

// IsAsset reports whether t carries a balance (payment_account, other_asset).
func (t Type) IsAsset() bool { return t.Kind() == KindAsset }

// IsLiability reports whether t carries a debt and a limit (credit_card, other_liability).
func (t Type) IsLiability() bool { return t.Kind() == KindLiability }

// Kind is "asset" or "liability" ("" for unknown types).
func (t Type) Kind() Kind {
	switch t {
	case TypePaymentAccount, TypeOtherAsset:
		return KindAsset
	case TypeCreditCard, TypeOtherLiability:
		return KindLiability
	}
	return KindUnknown
}

func typeList() string {
	s := make([]string, len(Types))
	for i, t := range Types {
		s[i] = string(t)
	}
	return strings.Join(s, ", ")
}
