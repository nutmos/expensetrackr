package balance

import "strings"

// Type is the kind of balance.
type Type string

const (
	PaymentAccount Type = "payment_account"
	CreditCard     Type = "credit_card"
	OtherAsset     Type = "other_asset"
	OtherLiability Type = "other_liability"
)

// Types lists every type in display order.
var Types = []Type{PaymentAccount, CreditCard, OtherAsset, OtherLiability}

// Valid reports whether t is a known type.
func (t Type) Valid() bool { return t.IsAsset() || t.IsLiability() }

type Kind string

const (
	Asset     Kind = "asset"
	Liability Kind = "liability"
)

// IsAsset reports whether t carries a balance (payment_account, other_asset).
func (t Type) IsAsset() bool { return t == PaymentAccount || t == OtherAsset }

// IsLiability reports whether t carries a debt and a limit (credit_card, other_liability).
func (t Type) IsLiability() bool { return t == CreditCard || t == OtherLiability }

// Kind is "asset" or "liability" ("" for unknown types).
func (t Type) Kind() Kind {
	switch t {
	case PaymentAccount, OtherAsset:
		return Asset
	case CreditCard, OtherLiability:
		return Liability
	}
	return ""
}

func typeList() string {
	s := make([]string, len(Types))
	for i, t := range Types {
		s[i] = string(t)
	}
	return strings.Join(s, ", ")
}
