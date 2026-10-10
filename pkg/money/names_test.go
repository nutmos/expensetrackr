package money

import "testing"

func TestCurrencyNamesCoverTable(t *testing.T) {
	for c := range currencyMinorUnits {
		if currencyNames[c] == "" {
			t.Errorf("missing name for %s", c)
		}
	}
	for c := range currencyNames {
		if _, ok := currencyMinorUnits[c]; !ok {
			t.Errorf("name for unsupported code %s", c)
		}
	}
	cs := Currencies()
	if len(cs) != len(currencyMinorUnits) || cs[0].Code > cs[1].Code {
		t.Fatalf("bad Currencies(): %d", len(cs))
	}
}
