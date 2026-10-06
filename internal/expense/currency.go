package expense

// currencyMinorUnits maps active ISO 4217 alphabetic codes to their number of
// minor-unit digits (the "exponent"). Most currencies use 2; the exceptions are
// listed explicitly. Source: ISO 4217 list one (active codes), precious metals
// and testing codes (XAU, XTS, ...) deliberately excluded.
var currencyMinorUnits = map[string]int{}

func init() {
	two := []string{
		"AED", "AFN", "ALL", "AMD", "ANG", "AOA", "ARS", "AUD", "AWG", "AZN",
		"BAM", "BBD", "BDT", "BGN", "BMD", "BND", "BOB", "BRL", "BSD", "BTN",
		"BWP", "BYN", "BZD", "CAD", "CDF", "CHF", "CNY", "COP", "CRC", "CUP",
		"CVE", "CZK", "DKK", "DOP", "DZD", "EGP", "ERN", "ETB", "EUR", "FJD",
		"FKP", "GBP", "GEL", "GHS", "GIP", "GMD", "GTQ", "GYD", "HKD", "HNL",
		"HTG", "HUF", "IDR", "ILS", "INR", "IRR", "JMD", "KES", "KGS", "KHR",
		"KPW", "KYD", "KZT", "LAK", "LBP", "LKR", "LRD", "LSL", "MAD", "MDL",
		"MGA", "MKD", "MMK", "MNT", "MOP", "MRU", "MUR", "MVR", "MWK", "MXN",
		"MYR", "MZN", "NAD", "NGN", "NIO", "NOK", "NPR", "NZD", "PAB", "PEN",
		"PGK", "PHP", "PKR", "PLN", "QAR", "RON", "RSD", "RUB", "SAR", "SBD",
		"SCR", "SDG", "SEK", "SGD", "SHP", "SLE", "SOS", "SRD", "SSP", "STN",
		"SVC", "SYP", "SZL", "THB", "TJS", "TMT", "TOP", "TRY", "TTD", "TWD",
		"TZS", "UAH", "USD", "UYU", "UZS", "VES", "WST", "XCD", "XCG", "YER",
		"ZAR", "ZMW", "ZWG",
	}
	zero := []string{
		"BIF", "CLP", "DJF", "GNF", "ISK", "JPY", "KMF", "KRW", "PYG", "RWF",
		"UGX", "UYI", "VND", "VUV", "XAF", "XOF", "XPF",
	}
	three := []string{"BHD", "IQD", "JOD", "KWD", "LYD", "OMR", "TND"}
	four := []string{"CLF", "UYW"}

	for _, c := range two {
		currencyMinorUnits[c] = 2
	}
	for _, c := range zero {
		currencyMinorUnits[c] = 0
	}
	for _, c := range three {
		currencyMinorUnits[c] = 3
	}
	for _, c := range four {
		currencyMinorUnits[c] = 4
	}
}

// MinorUnits returns the number of decimal places for an ISO 4217 code and
// whether the code is known.
func MinorUnits(code string) (int, bool) {
	n, ok := currencyMinorUnits[code]
	return n, ok
}
