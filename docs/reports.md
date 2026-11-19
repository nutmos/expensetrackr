# Reports

[← Back to the README](../README.md) · [All docs](README.md)

Read-only summaries of your transactions and balances (issue #136), on the
web page's **Reports** tab and in the API under `/api/reports`. Every amount
is exact (integer minor units summed with arbitrary precision) and reported
**per currency**: nothing is converted between currencies, so a user with THB
and SGD accounts sees one section for each.

| Report | Web page | API |
|---|---|---|
| Income & expenses | `/reports/income-expense?from=YYYY-MM-DD&to=YYYY-MM-DD` | `GET /api/reports/income-expense?from=<RFC 3339>&to=<RFC 3339>` |
| Assets & liabilities | `/reports/assets-liabilities` | `GET /api/reports/assets-liabilities` |

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
| `transfer`, `asset_purchase`, `repay_debt`, `balance_adjustment` | not counted (they move value between your balances or correct them) |

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

## Adding a report type

1. `pkg/report`: add an `Info` to `Catalog` and a `Build…` function (no I/O).
2. `pkg/store`: add the query it needs, if any.
3. `pkg/api/reports.go`: register `GET /api/reports/<id>`.
4. `pkg/apidoc/openapi.yaml`: document the path and response schema, and add
   the schema to `TestOpenAPIResponseShapes`.
5. `pkg/web/static/reports.js`: add it to `REPORTS`, and its
   `#report-<id>` block to `index.html`. The page route `/reports/<id>` is
   served for every id in `Catalog`.
