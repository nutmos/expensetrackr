# Web page

[← Back to the README](../README.md) · [All docs](README.md)

How the built-in web page works: pages and navigation, the transaction and
balance forms, and how it talks to the API.

Signed out, the page shows only a **Log in** card (or **Create your account**
on first run); signed in, the header shows the display name and a **Log out**
button. See [docs/auth.md](auth.md).

A small single-page app (vanilla JS, no build step) with real,
history-friendly URLs. Gin serves the same embedded `index.html` for every page
path, and `router.js` shows the view for `location.pathname`:

| URL                          | Page |
|------------------------------|------|
| `/`, `/transactions`         | Transactions list (browse only). Date filters live in the query: `/transactions?from=2026-10-01&to=2026-10-31`; they are whole days in the device's current time zone (sent to the API as `tz=<IANA zone>`). Without a range in the URL the list shows the current month, or the date range last applied on any transaction list within the past 24 hours (see below) |
| `/transactions/new`          | Add a transaction, step 1: pick the type (Expense, Income, Transfer, Asset Purchase, Return on Investment, Asset Sale, Repay Debt) |
| `/transactions/new?type=<type>` | Add a transaction, step 2: the form with that type selected (it can still be changed there). An unknown `type` shows the picker |
| `/transactions/<uid>/edit`   | Edit a transaction (same form) |
| `/balances`, `/balances/new`, `/balances/<uid>/edit` | Balances list / add / edit. `/balances?hidden=1` also lists hidden balances (the **Show hidden** box) |
| `/balances/<uid>`, `/balances/<uid>?from=…&to=…` | One balance's transactions (opened by clicking its name in the list); the only filter is a date range, defaulting like the transactions list |
| `/categories`, `/categories/new`, `/categories/<uid>/edit` | Categories list / add / edit |
| `/reports`, `/reports/income-expense?from=…&to=…`, `/reports/assets-liabilities`, `/reports/balance-transactions?balance=…&from=…&to=…` | Reports with bar charts (see [docs/reports.md](reports.md)); `/reports` opens Income & expenses |
| `/swagger/`                  | API documentation (Swagger UI; opens in a new tab from the header) |

**Default date range (issue #183).** The transactions list and a balance's
transactions page open on the current month (first to last day, device time
zone). When the user applies a date filter on either page (**Filter**, or
**Clear** for all time), the choice is saved in the browser's `localStorage`
(`expensetrackr.dateFilter`) for 24 hours and used by both pages whenever
their URL has no `from`/`to`. A range in the URL always wins; once the 24
hours pass, the default is the current month again.

Old hash links (`/#balances`, `/#categories`, `/#transactions`) are rewritten
to the matching path.

**API docs.** The header's **API docs** link opens `/swagger/` (Swagger UI)
in a new tab. The spec is hand-written (`pkg/apidoc/openapi.yaml`) and embedded
in the binary, and the Swagger UI assets are vendored (`swagger-ui-dist`
5.33.1, Apache-2.0), so the docs work without network access. A test
(`pkg/api/openapi_test.go`) fails when a `/api` route is missing from the
spec, or a spec operation has no route.

**Content Security Policy.** Every response carries a strict CSP
(`default-src 'self'`, no inline script or style, no framing) plus
`X-Content-Type-Options`, `X-Frame-Options` and `Referrer-Policy` (see
[auth.md](auth.md#security-headers)). Keep page code in `/static/*.js` and
`style.css`: inline `<script>`, `<style>`, `style=""` and `on…=""` attributes
are blocked. `el.style.x = …` from JS is fine. Swagger UI has its own, looser
CSP (inline styles and `data:` images).

**Currency dropdowns.** The transaction and balance forms pick the currency
from a searchable dropdown (the shared `combobox.js`) filled from
`GET /api/currencies`, so the page offers exactly the codes the server accepts
(`pkg/money`). Options read "🇹🇭 THB · Baht"; search matches code or name, and
the submitted value is always the plain code. On the
transaction form, choosing (or changing) the account sets the currency to that
balance's currency; it can still be changed, and the server's mismatch error is
shown. A new balance defaults to your base currency from the **Settings** page
(`USD` until you choose one).

**Currency flags.** Each currency in `GET /api/currencies` carries a `flag`
emoji (`pkg/money/flags.go`), shown before the code in the currency dropdowns,
the balance list and totals, the transaction list's Currency column and the
transfer form's "Received amount (🇸🇬 SGD)" label. The flag is the issuing
country's, built from the code's first two letters (ISO 4217 codes start with
the ISO 3166-1 alpha-2 country code: THB → TH → 🇹🇭), with overrides where that
is wrong: EUR → 🇪🇺; ANG and XCG (Netherlands Antillean / Caribbean guilder) →
🇨🇼 Curaçao; XAF, XOF, XCD, XPF (shared by several countries, no common flag)
and other supranational X… codes → 🌐. Flags are display only: requests and
responses elsewhere use plain codes. Systems without flag emoji (e.g. Windows)
show the two letters instead. Details: [docs/currencies.md](currencies.md).

**Phones and narrow screens** (issue #171). Below 640px wide the page
reflows instead of scrolling sideways: the header shows only the display name
(shortened) and **Log out**; the section tabs become a 3-column grid of
buttons so every section, API docs included, stays visible; and the lists
become one card per row: transactions, balances, categories, the balance
totals, the balance transactions report and API tokens. Tables that use the
generic `.stack-table` layout show each cell as a "Label value" line, the
labels copied from the column headers by `App.labelCells(table)`
(`router.js`); call it after rendering the rows of any new such table. Form
fields use 16px text there so iOS Safari does not zoom in on focus. The rules
apply to the screen only, so printing (Export PDF) keeps the full table.

**Uids are never shown on the page.** They identify records only behind the
scenes: in row `data-uid` attributes, dropdown option values, edit URLs
(`/<res>/<uid>/edit`) and API calls. Headings, rows, tooltips, messages and
confirmations use human labels instead. For example, an edit page is titled
"Edit transaction: 120.50 THB, 2026-10-08 12:30:00" or "Edit balance “Wallet”".

**Navigation**

- List pages are browse-only: list, filters, totals, and per-row **Edit** /
  **Delete** actions. There is no inline form.
- The **+ Add …** button at the top right of each list opens its add page.
  **Edit** on a row opens the edit page. Both are real links, so they can also
  open in a new tab.
- On a form page, **Save** (on success), **Cancel**, **← List** and **Esc**
  return to the list you came from, with its filters. The list then shows a
  confirmation message and highlights the saved row. This steps back in
  history, so the add/edit page does not stay in the back stack. A form page
  opened directly (bookmark/reload) replaces itself with the plain list
  instead.
- In-app links use `history.pushState`, so the browser back and forward
  buttons move between lists and form pages as expected. Every page can be
  reloaded or bookmarked.
- Validation errors (422/409) stay on the form page, shown next to each field.
- An edit page for a uid that no longer exists shows "does not exist" and
  hides the form.

**Transactions**

- The list is newest first, with a date filter (whole days in the device's time
  zone), a Type filter and per-currency totals. Expenses and income are summed
  separately (returns on investment count as income, the interest and fees of a debt
  repayment as spent); transfers, asset purchases, debt principal and balance
  adjustments are excluded from both. A repayment's interest and fees are shown
  under its amount.
- **Balance Adjustment** rows (recorded by manual balance edits) have their
  own style and label, a signed amount (+/−), and Edit/Delete like other
  rows. Their edit form keeps the type (shown but disabled), lets you pick
  any balance, and adds a **Direction** select (Increase / Decrease) with a
  hint saying whether it raises or lowers the balance or the debt. Saving
  moves the balance(s) by the difference; deleting (after a confirmation
  that says so) reverts the change.
- **Beginning Balance** rows (one per balance, recorded when it is created)
  look like balance adjustments, with a signed amount, but have **Edit only**
  (no Delete). Their edit form keeps the type and the balance (both shown,
  disabled), has the **Direction** select (Increase = started at the amount,
  Decrease = started below zero), and accepts an amount of 0. A transaction
  dated before it moves it back to that date. The new-balance form says that
  the starting amount is recorded this way.
- Columns: time (shown in the device's time zone, with the stored value on
  hover, and an "edited" marker), Type, Amount,
  Currency, Account ("from → to" for transfers, asset purchases and debt repayments, "investment → account" for returns on investment, "asset → account" for asset sales), Category, Note.
- The category name is looked up client-side from `/api/categories` by
  `category_uid`.
- The form has a **Type** selector (Expense / Income / Transfer / Asset Purchase / Return on Investment / Asset Sale / Repay Debt; balance
  adjustments cannot be entered, only edited). The source
  dropdown lists only the balance types allowed for that type, grouped under
  headings (Payment accounts, Credit cards, Other assets, Other liabilities).
  Each option shows the balance's name and current amount, e.g. "Wallet ·
  SGD 50.00" (typing a heading such as "card" still filters to that group). Its label
  changes to "Payment account", "Received into", "From (source)" or "Paid from". A **To
  (destination)** dropdown appears only for transfers (any balance) and asset
  purchases (other assets only); for a return on investment it is labelled
  **Investment** (other assets only), for an asset sale **Asset sold** (other
  assets only; the money is received into a payment account or credit card),
  and for a debt repayment **Debt repaid** (credit cards and other
  liabilities only; paid from a payment account).
- For a debt repayment the amount is labelled **Principal**, and an optional
  **Interest and fees** pair of inputs is shown, with the total paid from the
  account underneath. Only the principal lowers the debt.
- For an asset sale the money is received into a payment account or credit
  card only (other assets and liabilities are not offered). **Asset sold**
  and an **Entire asset sale** switch come right after it, before the amount:
  switching it on (or picking another asset while it is on) fills in the
  **Principal** with the asset's current balance, which can still be changed;
  saving hides the asset (see [Balances](balances.md)). The principal is what
  the asset goes down by. An optional **Gain / loss** input follows the
  currency (positive for a gain, negative such as `-20` for a loss), with the
  amount received underneath. The list shows the gain or loss, and "entire
  asset", under the amount.
- **Category** is required (except for transfers, where it is hidden, and
  debt repayments, where it appears as an optional "Category of interest &
  fees" only once interest or fees are filled in, and asset sales, where it
  appears as an optional "Category of the gain" (income categories) or
  "Category of the loss" (expense categories) once a gain or loss is filled
  in) and
  lists only categories of the selected type (expense categories for asset
  purchases, income categories for returns on investment). With no category
  of that type yet, a hint says to add one first. Editing an older
  transaction saved without a category asks for one before it can be saved.
- **Foreign currency amount (optional)**, shown for expenses, income, asset
  purchases and returns on investment: an amount plus a currency picker for
  what it cost or paid in another currency (e.g. a USD purchase charged to a
  THB card). It is for reference only; the list shows it in small text under
  the amount, and it is not part of the totals or balances.
- Time is entered in the device's local time zone. A preview shows the exact
  RFC 3339 value that will be saved, with the device's offset for that date.
- The add page remembers the last expense currency and account. Picking a
  balance fills in its currency (they must match).
- Saving moves the balance(s); a note under the form explains the rules. Edits
  send the loaded version, and a conflict (the transaction changed in another
  tab) shows a message with a **Reload latest transaction** button.

**Balances**

- The list is grouped by type, with counts. Assets show their balance.
  Liabilities show debt, limit and available. A red "over limit" badge marks
  debt above the limit. Asset amounts are shown in black and liability debts
  in amber, with the amount owed as a positive number; any negative amount
  (an overdrawn account, a card in credit) is shown in brackets, e.g.
  `(1,234.50)` (issue #188). There is also a per-currency totals table.
- Clicking a balance's name opens its transactions page (`/balances/<uid>`,
  issue #175): every transaction that moved the balance (as source or
  destination), newest first, with **In** / **Out** in the balance's currency
  and the balance (or debt) after each one, above a summary of the opening
  and closing amounts and the money in / out. The only filter is a **From** /
  **To** date range (whole days in the device's time zone, kept in the URL);
  with none, all transactions are shown. Each row has **Edit**, and saving or
  cancelling the transaction (or **Edit balance**) returns to this page. It
  uses `GET /api/reports/balance-transactions` (see [docs/reports.md](reports.md)).
- The form's amount fields follow the type: **Balance** for assets,
  **Debt** + **Limit** for liabilities. Hidden fields are not sent.
- On the edit page the **Type** selector is disabled, because types can't
  change after creation. A hint notes that typed amounts override the
  automatic ones and that a balance/debt change is recorded as a Balance
  Adjustment transaction. If the balance changed since the page was
  loaded (e.g. a transaction was saved), saving shows a conflict message with
  a **Reload latest values** button instead of overwriting.

**Categories**

- The list is grouped into expense and income categories.
- On the edit page the **Type** is locked (shown but disabled): a
  category's type is fixed when it is created. To use the other type, add a
  new category and delete the old one.
- **Delete** of a category still used by transactions shows the 409 message
  on the list.

**Reports**

- Sub-tabs pick the report. Charts are plain DOM bars (widths set from JS,
  which the CSP allows); each currency is its own section, never converted.
- **Income & expenses**: From / To dates or This month / Last month / This
  year / All time; Income vs Expenses bars, the net, then expenses and income
  by category with each one's share. The range is kept in the URL.
- **Assets & liabilities**: Assets vs Liabilities bars with the net worth,
  then one bar per account. Negative amounts are hatched and labelled
  "overdrawn" / "in credit".
- **Balance transactions**: pick a balance (hidden ones included) and a
  range like Income & expenses; a table of its transactions with In / Out
  and the running balance (debt for a liability). **Export PDF** opens the
  browser's print dialog ("Save as PDF"); the print stylesheet keeps only
  the statement. Balance and range are kept in the URL.

User data is only inserted with `textContent` / input `.value` (never
`innerHTML`), so HTML in notes or names is shown as text.
