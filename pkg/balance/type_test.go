package balance

import "testing"

func TestParseTypeAndKind(t *testing.T) {
	cases := []struct {
		in      string
		want    Type
		kind    Kind
		payable bool
	}{
		{" Payment_Account ", TypePaymentAccount, KindAsset, true},
		{"credit_card", TypeCreditCard, KindLiability, true},
		{"OTHER_ASSET", TypeOtherAsset, KindAsset, false},
		{"other_liability", TypeOtherLiability, KindLiability, false},
		{"loan", Type("loan"), KindUnknown, false},
		{"", Type(""), KindUnknown, false},
	}
	for _, c := range cases {
		got := ParseType(c.in)
		if got != c.want || got.Kind() != c.kind || got.IsPayable() != c.payable || got.Valid() != (c.kind != KindUnknown) {
			t.Errorf("ParseType(%q) = %q kind %q payable %v valid %v", c.in, got, got.Kind(), got.IsPayable(), got.Valid())
		}
	}
	if want := "payment_account, credit_card, other_asset, other_liability"; TypeList() != want {
		t.Errorf("TypeList() = %q, want %q", TypeList(), want)
	}
}

func TestFilter(t *testing.T) {
	items := []Balance{
		{Name: "a", Type: TypePaymentAccount},
		{Name: "b", Type: TypeOtherAsset, Hidden: true},
		{Name: "c", Type: TypeCreditCard, Hidden: true},
	}
	names := func(bs []Balance) (s string) {
		for _, b := range bs {
			s += b.Name
		}
		return s
	}
	if got := names(Filter(items, Shown)); got != "a" {
		t.Errorf("Filter(Shown) = %q, want a", got)
	}
	if got := names(Filter(items, Balance.IsPayable)); got != "ac" {
		t.Errorf("Filter(IsPayable) = %q, want ac", got)
	}
	if len(items) != 3 || items[1].Name != "b" {
		t.Errorf("Filter changed its input: %+v", items)
	}
}
