# Exchange rates and cross-currency transfers

A **transfer** may move money between two balances in different currencies
(e.g. a USD account to a THB account). Every other type (expense, income,
asset purchase, balance adjustment) still requires the transaction currency
to equal its balance(s)' currency.

## Fields

| JSON | Meaning |
|------|---------|
| `amount`, `currency` | Source side: leaves `balance_uid`, in the source balance's currency (must match, 422 otherwise). |
| `to_amount`, `to_amount_minor` | Amount credited to `to_balance_uid`, in `to_currency`, at that currency's scale. |
| `to_currency` | Destination balance's currency (server-derived; a client may send it, but it must match). |
| `exchange_rate` | `to_currency` units per 1 `currency` unit, decimal string, up to 10 significant digits. |
| `rate_source` | `frankfurter` (server fetched the ECB rate) or `manual` (client sent `to_amount`). |
| `rate_date` | `YYYY-MM-DD` the Frankfurter rate applies to; `null` for manual. |

All six are `null` for same-currency transfers and every other type. In the
database they are `to_amount_minor`, `to_amount_scale`, `to_currency`,
`exchange_rate` (TEXT), `rate_source`, `rate_date` — all nullable (part of
the baseline schema, `pkg/store/schema.go`).

## `conversion_rates` (computed, response-only)

Every transaction in a response (`GET` single and list, `POST`/`PUT`
responses, `409` conflict `current`, bulk `results[].transaction`, dry runs
included) also carries `conversion_rates`. For a cross-currency transfer it
shows **both directions** of the rate the transfer actually used, derived on
the fly from its own amounts; it is **not stored** (no column, no migration):

```json
"conversion_rates": [
  {"from": "THB", "to": "SGD", "rate": "0.0397"},
  {"from": "SGD", "to": "THB", "rate": "25.18891688"}
]
```

(Example from the tests: 1000.00 THB → 39.70 SGD, i.e. 1 THB = 0.0397 SGD
and 1 SGD = 25.18891688 THB.)

- Item 0 is always `currency` → `to_currency` = `to_amount / amount`; item 1
  is `to_currency` → `currency` = `amount / to_amount`. Each item has the same
  `from` / `to` / `rate` shape as `GET /api/exchange-rates`.
- `rate` is a decimal string computed exactly from the minor units and
  rounded half-up to 10 significant digits, the same format as
  `exchange_rate`.
- Item 0 equals `exchange_rate` for a manual rate. For a Frankfurter rate it
  can differ in the last digits, because `to_amount` was rounded to the
  destination's minor unit (e.g. 1 USD → 150 JPY); `conversion_rates` is the
  *effective* rate, `exchange_rate` the reference rate that was applied.
- It is `null` for **same-currency transfers** (no conversion happens; the
  rate would trivially be 1, and the other cross-currency fields are `null`
  too), for every other type, and if an amount were ever zero (no division
  by zero).
- It is read-only: like `account`, `amount_minor` or `exchange_rate`, sending
  it in a POST/PUT/bulk body is a 400 `unknown field "conversion_rates"`. Send
  only input fields (the web form does).

## Writing (POST / PUT `/api/transactions`)

Source and destination currencies **differ**:

- `to_amount` sent → manual: `exchange_rate = to_amount / amount`,
  `rate_source = "manual"`, `rate_date = null`. No network call.
- `to_amount` omitted → the server looks up the Frankfurter rate for the
  **local date of `spent_at`** (the date in the offset it was entered with;
  today or a future date uses the latest rate), computes
  `to_amount = amount × rate` rounded **half-up** to the destination
  currency's minor unit (e.g. 0 decimals for JPY), and stores the rate, source
  and the date Frankfurter reported.
- On PUT, omitting `to_amount` keeps the stored conversion when amount,
  currency, local date and both balances are unchanged (e.g. a note edit);
  otherwise the rate is fetched again. Sending `to_amount` makes it manual.

Source and destination currencies are **the same**: `to_amount` must be
omitted or equal `amount` (422 otherwise); nothing extra is stored.

### Failures

| Situation | Response |
|-----------|----------|
| Frankfurter has no rate for the pair (unsupported currency) | **422**, `code: exchange_rate_unsupported`, `fields.to_amount` — send `to_amount` manually |
| Frankfurter unreachable / timed out (5 s) / HTTP 5xx | **503**, `code: exchange_rate_unavailable` — retry or send `to_amount` |
| Frankfurter answered with something unexpected | **502**, `code: exchange_rate_unavailable` |
| `to_currency` ≠ destination currency, `to_amount` invalid / zero / wrong decimals | **422** on that field |

Nothing is written on any failure.

## Balance effects

The source changes by `amount` (source currency) exactly like before; the
destination changes by `to_amount` (destination currency) — an asset goes up,
a credit card / loan debt goes down. Editing or deleting reverses the
**stored** values; rates are never refetched for a reversal, so a later rate
change cannot leave a balance off by the difference. Stored rates are
history and are not updated.

Transfers are excluded from the per-currency totals on the transactions
page, so these are unaffected.

## Rate lookup: `GET /api/exchange-rates`

```
GET /api/exchange-rates?from=USD&to=THB&date=2026-10-10
200 {"from":"USD","to":"THB","rate":"33.62","date":"2026-10-09","source":"frankfurter"}
```

`date` is optional (latest). `from` = `to` returns rate `"1"` with
`source: "same_currency"`. Auth required, like every `/api` route. Errors as
in the table above (400 for a bad query). The web form uses it for the
"Received amount" preview.

## Frankfurter

Rates come from [Frankfurter](https://frankfurter.dev), a free, key-less API
serving the **European Central Bank** reference rates (data © ECB; thanks to
the Frankfurter project). Points to know:

- **Working days only.** The ECB publishes once per working day (around
  16:00 CET). Asking for a weekend or holiday returns the previous working
  day's rate, so `rate_date` can be earlier than the transfer's date. Before
  the day's publication, "today" returns yesterday's rate.
- **About 30 currencies** (EUR, USD, JPY, GBP, CHF, AUD, CAD, CNY, HKD, SGD,
  THB, KRW, INR, IDR, MYR, PHP, NZD, SEK, NOK, DKK, PLN, CZK, HUF, …). Pairs
  involving others (e.g. VND, TWD) are a 422; enter `to_amount` by hand.
- Reference (mid-market) rates, not what a bank or card actually charged —
  type the received amount when you know it.
- Base URL: `-fx-url` / `EXPENSE_FX_URL`, default
  `https://api.frankfurter.dev/v1` (the old `api.frankfurter.app` redirects
  there; redirects are followed). Requests: `GET {base}/{YYYY-MM-DD|latest}?from=USD&to=THB`.

## Cache (tables `fx_rates`, `fx_rate_lookups`)

- `fx_rates (base, quote, rate_date, rate, fetched_at)`, primary key
  `(base, quote, rate_date)`: every rate fetched, as the decimal text received.
- `fx_rate_lookups (base, quote, requested_date, rate_date, fetched_at)`:
  maps the requested date (or `latest`) to the date Frankfurter answered with.

A lookup is served from the cache when the requested date resolved to itself
or is more than 7 days old (final ECB data); `latest` and recent dates that
resolved to an earlier day are refetched after **3 hours** (the ECB may have
published since). So repeated transfers and previews for the same pair and
day do not hit the network. If the cache cannot be written the rate is still
used.

## Web UI

On the transaction form with type **Transfer**, the destination dropdown
lists balances of every currency; the currency dropdown still follows the
source. When the destination's currency differs, a **Received amount (THB)**
field appears, filled from `/api/exchange-rates` whenever the amount, date or
balances change (an amount change re-converts at once with the rate already
loaded). Typing in it switches to a manual amount ("Use market rate" switches
back); only then is `to_amount` sent.

Under the field, both directions of the conversion are shown, e.g.
`1 THB = 0.0397 SGD` and `1 SGD = 25.1889 THB`, updating live as Amount or
Received amount change and when the market-rate prefill arrives. While
typing they are computed in the browser with the server's formula
(`to_amount / amount` and its inverse, exact on the minor units); when an
existing transaction is opened for editing, the API's `conversion_rates` are
shown as returned. Rates are displayed with 6 significant digits (never
fewer than their whole-number digits); the full 10-digit value is in the
tooltip. They are display-only: `conversion_rates` is never sent. The field
and the rates are hidden for same-currency transfers and every other type.

The list shows cross-currency transfers as `1,000.00 THB → 39.70 SGD` (the
two amounts may wrap onto two lines), with both rates from
`conversion_rates` underneath (`1 THB = 0.0397 SGD`, `1 SGD = 25.1889 THB`)
and the full values plus the rate source in the tooltip. On narrow screens
(≤ 640 px) each transaction is shown as a compact card: details on the left,
the amounts and both rates on the right.

## Limitations

- Only transfers convert; a foreign-currency expense on a card still needs a
  balance in that currency.
- Only ECB currencies can be converted automatically.
- No per-transaction fee field; fold fees into `to_amount` (manual).
