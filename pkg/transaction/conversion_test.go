package transaction

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func crossTransfer(amountMinor int64, cur string, toMinor int64, toCur string) Transaction {
	return Transaction{Type: Transfer, Currency: cur, AmountMinor: amountMinor, ToCurrency: &toCur, ToAmountMinor: &toMinor}
}

func TestComputeConversionRates(t *testing.T) {
	cases := []struct {
		name string
		e    Transaction
		want []ConversionRate
	}{
		{"THB to SGD", crossTransfer(100000, "THB", 3970, "SGD"), []ConversionRate{
			{From: "THB", To: "SGD", Rate: "0.0397"}, {From: "SGD", To: "THB", Rate: "25.18891688"}}},
		{"USD to THB (Frankfurter-rounded)", crossTransfer(10000, "USD", 336150, "THB"), []ConversionRate{
			{From: "USD", To: "THB", Rate: "33.615"}, {From: "THB", To: "USD", Rate: "0.02974862413"}}},
		{"scale 2 to scale 0", crossTransfer(100, "USD", 150, "JPY"), []ConversionRate{
			{From: "USD", To: "JPY", Rate: "150"}, {From: "JPY", To: "USD", Rate: "0.006666666667"}}},
	}
	for _, c := range cases {
		if got := c.e.ComputeConversionRates(); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestComputeConversionRatesNil(t *testing.T) {
	sgd := "SGD"
	zero := int64(0)
	asset := crossTransfer(100000, "THB", 3970, "SGD")
	asset.Type = AssetPurchase
	unknown := crossTransfer(100, "THB", 100, "XXQ")
	noAmount := crossTransfer(0, "THB", 3970, "SGD")
	noTo := crossTransfer(100000, "THB", 0, "SGD")
	for name, e := range map[string]Transaction{
		"expense":                  {Type: Expense, Currency: "THB", AmountMinor: 100},
		"same-currency transfer":   {Type: Transfer, Currency: "SGD", AmountMinor: 100},
		"same currency in to_*":    crossTransfer(100, "SGD", 100, "SGD"),
		"asset purchase":           asset,
		"missing to_amount":        {Type: Transfer, Currency: "THB", AmountMinor: 100, ToCurrency: &sgd},
		"zero to_amount pointer":   {Type: Transfer, Currency: "THB", AmountMinor: 100, ToCurrency: &sgd, ToAmountMinor: &zero},
		"zero amount":              noAmount,
		"zero to_amount":           noTo,
		"unknown currency":         unknown,
		"balance adjustment":       {Type: BalanceAdjustment, Currency: "THB", AmountMinor: 100},
		"empty (zero value) value": {},
	} {
		if got := e.ComputeConversionRates(); got != nil {
			t.Errorf("%s: got %+v, want nil", name, got)
		}
	}
}

func TestMarshalJSONComputesConversionRates(t *testing.T) {
	e := crossTransfer(100000, "THB", 3970, "SGD")
	e.UID = "u"
	// A value already in the field (e.g. decoded from a client) is ignored.
	e.ConversionRates = []ConversionRate{{From: "XXX", To: "YYY", Rate: "42"}}
	for _, v := range []any{e, &e, []Transaction{e}} {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), `"conversion_rates":[{"from":"THB","to":"SGD","rate":"0.0397"},{"from":"SGD","to":"THB","rate":"25.18891688"}]`) {
			t.Errorf("%T: %s", v, b)
		}
		if strings.Contains(string(b), "XXX") {
			t.Errorf("%T: stale value encoded: %s", v, b)
		}
	}
	// Other fields are encoded unchanged (same keys as the plain struct).
	b, _ := json.Marshal(Transaction{Type: Expense, Currency: "THB"})
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if v, ok := m["conversion_rates"]; !ok || v != nil {
		t.Errorf("expense conversion_rates = %v (present %v), want null", v, ok)
	}
	for _, k := range []string{"uid", "amount", "exchange_rate", "import_ref", "version"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing key %s in %s", k, b)
		}
	}
	if _, ok := m["SpentTime"]; ok {
		t.Errorf("internal field encoded: %s", b)
	}
}
