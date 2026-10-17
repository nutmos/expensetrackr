# Currencies and flags

The server accepts the ISO 4217 codes listed in
[`pkg/money/currency.go`](../pkg/money/currency.go) (with English names in
`names.go`). `GET /api/currencies` returns them sorted by code:

```json
{"currencies":[{"code":"THB","name":"Baht","scale":2,"flag":"🇹🇭"}, ...],"count":159}
```

- `scale`: number of decimals allowed in amounts (JPY 0, THB 2, KWD 3, CLF 4).
- `flag`: a **display-only** emoji for the issuing country or region. Every
  other request and response uses the plain code (`"currency": "THB"`); never
  send the flag.

## How flags are derived

[`pkg/money/flags.go`](../pkg/money/flags.go), `money.Flag(code)`:

1. Most ISO 4217 codes start with the ISO 3166-1 alpha-2 code of the issuing
   country (THB → TH, USD → US, GBP → GB). Those two letters become a pair of
   Unicode regional indicator symbols (U+1F1E6 + letter offset), which
   platforms render as the country's flag: TH → 🇹🇭.
2. The prefix is only used when it is an assigned ISO 3166-1 code (list in
   `regionCodes`); otherwise `Flag` returns `""` (e.g. XAU, XTS, made-up codes).
3. Explicit overrides (`flagOverrides`) where the prefix is wrong:

| Code | Flag | Why |
| ---- | ---- | --- |
| EUR | 🇪🇺 | Euro; European Union flag |
| ANG | 🇨🇼 | Netherlands Antillean guilder; AN no longer exists, used in Curaçao and Sint Maarten |
| XCG | 🇨🇼 | Caribbean guilder (ANG's successor), Curaçao and Sint Maarten |
| XAF | 🌐 | CFA franc BEAC, six Central African states, no common flag |
| XOF | 🌐 | CFA franc BCEAO, eight West African states, no common flag |
| XCD | 🌐 | East Caribbean dollar, eight ECCU members, no common flag |
| XPF | 🌐 | CFP franc: French Polynesia, New Caledonia, Wallis and Futuna |
| XDR, XSU, XUA | 🌐 | Supranational units (not currently accepted; covered for completeness) |

`pkg/money/flags_test.go` lists the expected flag for **every** supported
code, so adding a currency without checking its flag fails the tests.

Platforms without flag emoji (notably Windows) show the two regional letters
(e.g. "TH") instead of a picture; the code is always shown next to it.

## In the web page

- Currency dropdowns (transaction and balance forms): options and the
  selected value read "🇹🇭 THB · Baht". Search matches code or name; the
  submitted value is the plain code.
- Balances list: the Currency column and the "Totals per currency" table.
- Transactions list: the Currency column ("🇹🇭 THB", or "🇹🇭 THB → 🇸🇬 SGD"
  for a cross-currency transfer) and the per-currency totals line.
- Transfer form: "Received amount (🇸🇬 SGD)".
