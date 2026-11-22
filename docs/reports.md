# Reports

[← Back to the README](../README.md) · [All docs](README.md)

Read-only summaries of your transactions and balances (issues #136, #160), on the
web page's **Reports** tab and in the API under `/api/reports`. Every amount
is exact (integer minor units summed with arbitrary precision) and reported
**per currency**: nothing is converted between currencies, so a user with THB
and SGD accounts sees one section for each.

| Report | Web page | API |
|---|---|---|
| Income & expenses | `/reports/income-expense?from=YYYY-MM-DD&to=YYYY-MM-DD` | `GET /api/reports/income-expense?from=<RFC 3339>&to=<RFC 3339>` |
| Assets & liabilities | `/reports/assets-liabilities` | `GET /api/reports/assets-liabilities` |
| Balance transactions | `/reports/balance-transactions?balance=<uid>&from=YYYY-MM-DD&to=YYYY-MM-DD` | `GET /api/reports/balance-transactions?balance_uid=<uid>&from=<RFC 3339>&to=<RFC 3339>` |

`GET /api/reports` lists the available reports (`id`, `name`, `description`,
`path`), so clients can discover new ones as they are added. `/reports`
opens the first one.

## Income & expenses

Sums the transactions spent in a time range. Which transactions count is the
same rule as the totals under the transaction list:

| Type | Counted as |
|---|---|
| `expense` | expense |
| `income`, `return_on_investment` | income |
| `repay_debt` | its `interest_amount` + `fee_amount` count as **expense**, under its category (or no category); the principal is not counted |
| `asset_sale` | its `gain_loss` counts as **income** when positive (a gain) and as **expense** when negative (a loss), under its category (or no category); the principal is not counted |
| `transfer`, `asset_purchase`, `balance_adjustment`, `beginning_balance` | not counted (they move value between your balances, correct them or start them) |

`from` and `to` are optional, inclusive bounds on `spent_at` (RFC 3339 with
offset, like the transaction list filter); omit both for all time. A bad
value, or `to` earlier than `from`, is **400** with `fields`.

For each currency the response has `income`, `expense`, `net`
(income − expense, may be negative), the transaction counts, and both sides
broken down by category, largest first. A category used by both income and
returns on investment is one entry. Transactions saved before categories
were required are grouped under `category_uid: null` / `category_name: null`.
`amount_minor` is a **string** here, since a sum may exceed 64 bits.

```bash
curl -s -b jar 'http://127.0.0.1:8080/api/reports/income-expense?from=2026-11-01T00:00:00%2B07:00&to=2026-11-30T23:59:59%2B07:00'
```

```json
{
  "report": "income-expense",
  "from": "2026-11-01T00:00:00+07:00",
  "to": "2026-11-30T23:59:59+07:00",
  "currencies": [
    {
      "currency": "THB",
      "income": "74250.00", "expense": "28071.25", "net": "46178.75",
      "income_count": 3, "expense_count": 10,
      "income_by_category": [
        { "category_uid": "…", "category_name": "Salary", "amount": "65000.00", "amount_minor": "6500000", "count": 1 }
      ],
      "expense_by_category": [
        { "category_uid": "…", "category_name": "Rent", "amount": "18000.00", "amount_minor": "1800000", "count": 1 }
      ]
    }
  ]
}
```

On the web page the range is picked with **From** / **To** dates (whole days
in the device's time zone) or the **This month**, **Last month**, **This
year** and **All time** buttons; it is kept in the URL (`?all=1` for all
time) so it can be bookmarked. With no range in the URL the page shows the
current month. Each currency gets an Income vs Expenses bar chart, the net,
and an "Expenses by category" and "Income by category" chart with each
category's share.

## Assets & liabilities

Your balances as they are now. Per currency:

- `assets`: the sum of `payment_account` and `other_asset` balances;
- `liabilities`: the sum of `credit_card` and `other_liability` debts;
- `net`: assets − liabilities (net worth in that currency);
- `by_type`: the total and count per balance type present;
- `accounts`: every balance with its `amount` (balance of an asset, debt of
  a liability), assets first then liabilities, each largest first.

Amounts may be negative: an overdrawn account, or a card in credit. `as_of`
is the server time of the report.

The web page draws an Assets vs Liabilities bar chart with the net worth,
then a "By account" chart (assets green, liabilities red). Negative amounts
are drawn hatched and labelled "overdrawn" or "in credit".

## Balance transactions

A statement of one balance (issue #160): every transaction that moved it
(as `balance_uid` or `to_balance_uid`) with `spent_at` in the range, oldest
first. Hidden balances can be reported on. `balance_uid` is required (400
when missing or not a UUID, 404 when it is not one of yours); `from` / `to`
work like the income & expense report.

All amounts are in the **balance's** currency (a cross-currency transfer into
it counts its `to_amount`). For each transaction:

- `money_in` / `money_out` (one of them is set): what it did to the balance's
  value. For an asset, in = deposits, out = spending. For a liability, in
  lowers the debt (payments) and out raises it (charges). A debt repayment
  takes principal + interest + fees out of the paying account and puts only
  the principal into the liability.
- `amount_after`: the balance (asset) or debt (liability) right after it.
- `counterparty`: the other balance of a transfer, asset purchase, return on
  investment or debt repayment; `category_name`, `note`, and the
  transaction's own `amount` / `currency` as recorded.

The report also has `opening_amount` and `closing_amount`, the totals
`money_in`, `money_out`, `net` and `count`, and the `balance` itself
(`uid`, `name`, `type`, `kind`, `currency`, `hidden` and its current
`amount`). The closing amount is worked back from the current amount minus
every change made after the range, and the opening amount from the closing
amount minus the range's net, so both are right even with later
transactions.

The balance's **beginning balance** is listed like any other transaction (it
is always the earliest one, before any transaction at the same time), so a
report over all time opens at 0 and its first row brings the balance to its
starting amount.

```bash
curl -s -b jar 'http://127.0.0.1:8080/api/reports/balance-transactions?balance_uid=<uid>&from=2026-11-01T00:00:00%2B07:00&to=2026-11-30T23:59:59%2B07:00'
```

On the web page, pick the balance and the range (same buttons as income &
expenses). **Export PDF** opens the browser's print dialog; choose "Save as
PDF". The print stylesheet keeps only the statement (no navigation or
filters) and the suggested file name is "<balance> transactions <from> to
<to>". The PDF is made by the browser, so names and notes in any script
(Thai included) print correctly without fonts on the server.

## Adding a report type

1. `pkg/report`: add an `Info` to `Catalog` and a `Build…` function (no I/O).
2. `pkg/store`: add the query it needs, if any.
3. `pkg/api/reports.go`: register `GET /api/reports/<id>`.
4. `pkg/apidoc/openapi.yaml`: document the path and response schema, and add
   the schema to `TestOpenAPIResponseShapes`.
5. `pkg/web/static/reports.js`: add it to `REPORTS`, and its
   `#report-<id>` block to `index.html`. The page route `/reports/<id>` is
   served for every id in `Catalog`.
