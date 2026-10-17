package money

import (
	"testing"
	"unicode/utf8"
)

// wantFlags lists the expected flag for every supported currency, reviewed by
// hand (see flagOverrides for the codes that are not the country prefix).
var wantFlags = map[string]string{
	"AED": "🇦🇪", "AFN": "🇦🇫", "ALL": "🇦🇱", "AMD": "🇦🇲", "ANG": "🇨🇼", "AOA": "🇦🇴",
	"ARS": "🇦🇷", "AUD": "🇦🇺", "AWG": "🇦🇼", "AZN": "🇦🇿", "BAM": "🇧🇦", "BBD": "🇧🇧",
	"BDT": "🇧🇩", "BGN": "🇧🇬", "BHD": "🇧🇭", "BIF": "🇧🇮", "BMD": "🇧🇲", "BND": "🇧🇳",
	"BOB": "🇧🇴", "BRL": "🇧🇷", "BSD": "🇧🇸", "BTN": "🇧🇹", "BWP": "🇧🇼", "BYN": "🇧🇾",
	"BZD": "🇧🇿", "CAD": "🇨🇦", "CDF": "🇨🇩", "CHF": "🇨🇭", "CLF": "🇨🇱", "CLP": "🇨🇱",
	"CNY": "🇨🇳", "COP": "🇨🇴", "CRC": "🇨🇷", "CUP": "🇨🇺", "CVE": "🇨🇻", "CZK": "🇨🇿",
	"DJF": "🇩🇯", "DKK": "🇩🇰", "DOP": "🇩🇴", "DZD": "🇩🇿", "EGP": "🇪🇬", "ERN": "🇪🇷",
	"ETB": "🇪🇹", "EUR": "🇪🇺", "FJD": "🇫🇯", "FKP": "🇫🇰", "GBP": "🇬🇧", "GEL": "🇬🇪",
	"GHS": "🇬🇭", "GIP": "🇬🇮", "GMD": "🇬🇲", "GNF": "🇬🇳", "GTQ": "🇬🇹", "GYD": "🇬🇾",
	"HKD": "🇭🇰", "HNL": "🇭🇳", "HTG": "🇭🇹", "HUF": "🇭🇺", "IDR": "🇮🇩", "ILS": "🇮🇱",
	"INR": "🇮🇳", "IQD": "🇮🇶", "IRR": "🇮🇷", "ISK": "🇮🇸", "JMD": "🇯🇲", "JOD": "🇯🇴",
	"JPY": "🇯🇵", "KES": "🇰🇪", "KGS": "🇰🇬", "KHR": "🇰🇭", "KMF": "🇰🇲", "KPW": "🇰🇵",
	"KRW": "🇰🇷", "KWD": "🇰🇼", "KYD": "🇰🇾", "KZT": "🇰🇿", "LAK": "🇱🇦", "LBP": "🇱🇧",
	"LKR": "🇱🇰", "LRD": "🇱🇷", "LSL": "🇱🇸", "LYD": "🇱🇾", "MAD": "🇲🇦", "MDL": "🇲🇩",
	"MGA": "🇲🇬", "MKD": "🇲🇰", "MMK": "🇲🇲", "MNT": "🇲🇳", "MOP": "🇲🇴", "MRU": "🇲🇷",
	"MUR": "🇲🇺", "MVR": "🇲🇻", "MWK": "🇲🇼", "MXN": "🇲🇽", "MYR": "🇲🇾", "MZN": "🇲🇿",
	"NAD": "🇳🇦", "NGN": "🇳🇬", "NIO": "🇳🇮", "NOK": "🇳🇴", "NPR": "🇳🇵", "NZD": "🇳🇿",
	"OMR": "🇴🇲", "PAB": "🇵🇦", "PEN": "🇵🇪", "PGK": "🇵🇬", "PHP": "🇵🇭", "PKR": "🇵🇰",
	"PLN": "🇵🇱", "PYG": "🇵🇾", "QAR": "🇶🇦", "RON": "🇷🇴", "RSD": "🇷🇸", "RUB": "🇷🇺",
	"RWF": "🇷🇼", "SAR": "🇸🇦", "SBD": "🇸🇧", "SCR": "🇸🇨", "SDG": "🇸🇩", "SEK": "🇸🇪",
	"SGD": "🇸🇬", "SHP": "🇸🇭", "SLE": "🇸🇱", "SOS": "🇸🇴", "SRD": "🇸🇷", "SSP": "🇸🇸",
	"STN": "🇸🇹", "SVC": "🇸🇻", "SYP": "🇸🇾", "SZL": "🇸🇿", "THB": "🇹🇭", "TJS": "🇹🇯",
	"TMT": "🇹🇲", "TND": "🇹🇳", "TOP": "🇹🇴", "TRY": "🇹🇷", "TTD": "🇹🇹", "TWD": "🇹🇼",
	"TZS": "🇹🇿", "UAH": "🇺🇦", "UGX": "🇺🇬", "USD": "🇺🇸", "UYI": "🇺🇾", "UYU": "🇺🇾",
	"UYW": "🇺🇾", "UZS": "🇺🇿", "VES": "🇻🇪", "VND": "🇻🇳", "VUV": "🇻🇺", "WST": "🇼🇸",
	"XAF": "🌐", "XCD": "🌐", "XCG": "🇨🇼", "XOF": "🌐", "XPF": "🌐", "YER": "🇾🇪",
	"ZAR": "🇿🇦", "ZMW": "🇿🇲", "ZWG": "🇿🇼",
}

func TestFlagEverySupportedCode(t *testing.T) {
	for c := range currencyMinorUnits {
		want, ok := wantFlags[c]
		if !ok {
			t.Errorf("wantFlags has no entry for supported code %s", c)
			continue
		}
		if got := Flag(c); got != want {
			t.Errorf("Flag(%s) = %q, want %q", c, got, want)
		}
	}
	for c := range wantFlags {
		if _, ok := currencyMinorUnits[c]; !ok {
			t.Errorf("wantFlags lists unsupported code %s", c)
		}
	}
	for _, cur := range Currencies() {
		if cur.Flag == "" || cur.Flag != Flag(cur.Code) {
			t.Errorf("Currencies(): %s has flag %q", cur.Code, cur.Flag)
		}
	}
}

func TestFlag(t *testing.T) {
	for code, want := range map[string]string{
		"THB": "\U0001F1F9\U0001F1ED", // regional indicators T, H
		"thb": "🇹🇭", " usd ": "🇺🇸", "EUR": "🇪🇺", "ANG": "🇨🇼", "XCG": "🇨🇼",
		"XAF": "🌐", "XOF": "🌐", "XCD": "🌐", "XPF": "🌐", "XDR": "🌐",
		"XAU": "", "XTS": "", "ZZZ": "", "AAA": "", "": "", "TH": "", "THBX": "",
	} {
		if got := Flag(code); got != want {
			t.Errorf("Flag(%q) = %q, want %q", code, got, want)
		}
	}
}

func TestFlagOverridesAreNeeded(t *testing.T) {
	// Overrides are values Flag can render: a region code or the neutral flag.
	for code, v := range flagOverrides {
		if !regionCodes[v] && v != neutralFlag {
			t.Errorf("override %s = %q is neither a region code nor the neutral flag", code, v)
		}
	}
	if n := len(regionCodes); n != 250 { // 249 ISO 3166-1 codes + EU
		t.Errorf("regionCodes has %d entries, want 250", n)
	}
	if utf8.RuneCountInString(regionFlag("TH")) != 2 {
		t.Error("regionFlag should yield two runes")
	}
}
