package money

import "strings"

// Flag emoji for currencies (GET /api/currencies "flag").
//
// Most ISO 4217 codes start with the ISO 3166-1 alpha-2 code of the issuing
// country (THB → TH, USD → US), so the flag is derived from those two letters
// as a pair of Unicode regional indicator symbols, which platforms render as
// the country's flag. flagOverrides covers the codes where that is wrong:
// a supranational issuer (EUR, the X… codes) or a prefix that is no longer a
// country (ANG, "Netherlands Antilles").

// neutralFlag marks currencies shared by several countries that have no flag
// of their own (CFA francs, East Caribbean dollar, CFP franc, SDR, ...).
const neutralFlag = "🌐"

// flagOverrides maps a currency code to its emoji when the code's first two
// letters do not name the right flag. The value is either a region code
// (rendered as regional indicators) or a literal emoji.
var flagOverrides = map[string]string{
	"EUR": "EU",        // Euro: European Union flag (EU is an exceptionally reserved ISO 3166 code)
	"ANG": "CW",        // Netherlands Antillean guilder: Curaçao (and Sint Maarten); AN no longer exists
	"XCG": "CW",        // Caribbean guilder, ANG's successor: Curaçao (and Sint Maarten)
	"XAF": neutralFlag, // CFA franc BEAC: six Central African states
	"XOF": neutralFlag, // CFA franc BCEAO: eight West African states
	"XCD": neutralFlag, // East Caribbean dollar: eight ECCU members
	"XPF": neutralFlag, // CFP franc: French Polynesia, New Caledonia, Wallis and Futuna
	"XDR": neutralFlag, // IMF Special Drawing Right
	"XSU": neutralFlag, // SUCRE (ALBA)
	"XUA": neutralFlag, // ADB Unit of Account
}

// regionCodes holds the officially assigned ISO 3166-1 alpha-2 codes, plus EU
// (exceptionally reserved; it has an emoji flag).
var regionCodes = func() map[string]bool {
	const list = "AD AE AF AG AI AL AM AO AQ AR AS AT AU AW AX AZ BA BB BD BE BF BG BH BI BJ BL BM BN BO BQ BR BS BT BV BW BY BZ " +
		"CA CC CD CF CG CH CI CK CL CM CN CO CR CU CV CW CX CY CZ DE DJ DK DM DO DZ EC EE EG EH ER ES ET EU FI FJ FK FM FO FR " +
		"GA GB GD GE GF GG GH GI GL GM GN GP GQ GR GS GT GU GW GY HK HM HN HR HT HU ID IE IL IM IN IO IQ IR IS IT JE JM JO JP " +
		"KE KG KH KI KM KN KP KR KW KY KZ LA LB LC LI LK LR LS LT LU LV LY MA MC MD ME MF MG MH MK ML MM MN MO MP MQ MR MS MT " +
		"MU MV MW MX MY MZ NA NC NE NF NG NI NL NO NP NR NU NZ OM PA PE PF PG PH PK PL PM PN PR PS PT PW PY QA RE RO RS RU RW " +
		"SA SB SC SD SE SG SH SI SJ SK SL SM SN SO SR SS ST SV SX SY SZ TC TD TF TG TH TJ TK TL TM TN TO TR TT TV TW TZ UA UG " +
		"UM US UY UZ VA VC VE VG VI VN VU WF WS YE YT ZA ZM ZW"
	m := map[string]bool{}
	for _, c := range strings.Fields(list) {
		m[c] = true
	}
	return m
}()

// regionFlag turns a two-letter region code into its flag emoji
// ("TH" → U+1F1F9 U+1F1ED = 🇹🇭).
func regionFlag(region string) string {
	var b strings.Builder
	for _, r := range region {
		b.WriteRune(0x1F1E6 + (r - 'A'))
	}
	return b.String()
}

// Flag returns the flag emoji shown next to an ISO 4217 code: the issuing
// country's flag, 🇪🇺 for EUR, or 🌐 for currencies shared by several
// countries. It returns "" for a code it cannot place (not three letters, or
// an unknown X… code whose prefix is not a country).
func Flag(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	if len(code) != 3 {
		return ""
	}
	if o, ok := flagOverrides[code]; ok {
		if regionCodes[o] {
			return regionFlag(o)
		}
		return o
	}
	if region := code[:2]; regionCodes[region] {
		return regionFlag(region)
	}
	return ""
}
