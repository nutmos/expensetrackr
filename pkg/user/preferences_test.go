package user

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nutmos/expensetrackr/pkg/validate"
)

func TestDecodePreferences(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		want      UserPreferences
		wantField string // "" = valid
	}{
		{"absent", ``, UserPreferences{}, ""},
		{"null", `null`, UserPreferences{}, ""},
		{"empty object", `{}`, UserPreferences{}, ""},
		{"base currency", `{"base_currency":"EUR"}`, UserPreferences{BaseCurrency: "EUR"}, ""},
		{"normalized code", `{ "base_currency" : " jpy " }`, UserPreferences{BaseCurrency: "JPY"}, ""},
		{"empty code means default", `{"base_currency":""}`, UserPreferences{}, ""},
		{"unknown currency", `{"base_currency":"ABC"}`, UserPreferences{}, "base_currency"},
		{"testing code excluded", `{"base_currency":"XTS"}`, UserPreferences{}, "base_currency"},
		{"wrong type", `{"base_currency":42}`, UserPreferences{}, "base_currency"},
		{"unknown key", `{"theme":"dark"}`, UserPreferences{}, "preferences"},
		{"array", `[1,2]`, UserPreferences{}, "preferences"},
		{"string", `"x"`, UserPreferences{}, "preferences"},
		{"number", `42`, UserPreferences{}, "preferences"},
		{"too big", `{"base_currency":"` + strings.Repeat("x", MaxPreferencesSize) + `"}`, UserPreferences{}, "preferences"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodePreferences(json.RawMessage(tc.in))
			if tc.wantField == "" {
				if err != nil || got != tc.want {
					t.Fatalf("got %+v, %v; want %+v", got, err, tc.want)
				}
				return
			}
			var ve *validate.ValidationError
			if !errors.As(err, &ve) || ve.Fields[tc.wantField] == "" {
				t.Fatalf("want validation error on %q, got %v", tc.wantField, err)
			}
		})
	}
}

func TestParseStoredPreferences(t *testing.T) {
	tests := []struct {
		name    string
		stored  string
		want    UserPreferences
		wantErr bool
	}{
		{"empty column", ``, UserPreferences{}, false},
		{"empty object", `{}`, UserPreferences{}, false},
		{"set", `{"base_currency":"SGD"}`, UserPreferences{BaseCurrency: "SGD"}, false},
		{"unknown keys ignored", `{"theme":"dark","base_currency":"THB","x":{"y":[1]}}`, UserPreferences{BaseCurrency: "THB"}, false},
		{"corrupt", `{bad`, UserPreferences{}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseStoredPreferences([]byte(tc.stored))
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("got %+v, %v", got, err)
			}
		})
	}
}

func TestPreferencesJSON(t *testing.T) {
	tests := []struct {
		name       string
		p          UserPreferences
		wantAPI    string
		wantStored string
	}{
		{"default", UserPreferences{}, `{"base_currency":"` + DefaultBaseCurrency + `"}`, `{}`},
		{"set", UserPreferences{BaseCurrency: "EUR"}, `{"base_currency":"EUR"}`, `{"base_currency":"EUR"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			api, err := json.Marshal(tc.p)
			if err != nil || string(api) != tc.wantAPI {
				t.Errorf("API JSON %s %v, want %s", api, err, tc.wantAPI)
			}
			if got := tc.p.StoredJSON(); got != tc.wantStored {
				t.Errorf("stored %s, want %s", got, tc.wantStored)
			}
		})
	}
}

func TestDefaultBaseCurrencySupported(t *testing.T) {
	if _, err := (UserPreferences{BaseCurrency: DefaultBaseCurrency}).Normalize(); err != nil {
		t.Fatalf("default %s is not a supported currency: %v", DefaultBaseCurrency, err)
	}
}
