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
| `/`, `/transactions`         | Transactions list (browse only). Date filters live in the query: `/transactions?from=2026-10-01&to=2026-10-31` |
| `/transactions/new`          | Add a transaction |
| `/transactions/<uid>/edit`   | Edit a transaction (same form) |
| `/balances`, `/balances/new`, `/balances/<uid>/edit` | Balances list / add / edit |
| `/categories`, `/categories/new`, `/categories/<uid>/edit` | Categories list / add / edit |
| `/swagger/`                  | API documentation (Swagger UI; opens in a new tab from the header) |

Old hash links (`/#balances`, `/#categories`, `/#transactions`) are rewritten
to the matching path.

**API docs.** The header's **API docs** link opens `/swagger/` (Swagger UI)
in a new tab. The spec is hand-written (`pkg/apidoc/openapi.yaml`) and embedded
in the binary, and the Swagger UI assets are vendored (`swagger-ui-dist`
5.33.1, Apache-2.0), so the docs work without network access. A test
(`pkg/api/openapi_test.go`) fails when a `/api` route is missing from the
spec, or a spec operation has no route.

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
  separately (returns on investment count as income); transfers, asset purchases and
  balance adjustments are excluded from both.
- **Balance Adjustment** rows (recorded by manual balance edits) have their
  own style and label, a signed amount (+/−), and "Automatic" instead of
  Edit/Delete; their edit URL shows a read-only message.
- Columns: time (shown in the device's time zone, with the stored value on
  hover, and an "edited" marker), Type, Amount,
  Currency, Account ("from → to" for transfers and asset purchases, "investment → account" for returns on investment), Category, Note.
- The category name is looked up client-side from `/api/categories` by
  `category_uid`.
- The form has a **Type** selector (Expense / Income / Transfer / Asset Purchase / Return on Investment; balance
  adjustments cannot be entered). The source
  dropdown lists only the balance types allowed for that type, grouped under
  headings (Payment accounts, Credit cards, Other assets, Other liabilities).
  Each option shows the balance's name and current amount, e.g. "Wallet ·
  SGD 50.00" (typing a heading such as "card" still filters to that group). Its label
  changes to "Payment account", "Received into", "From (source)" or "Paid from". A **To
  (destination)** dropdown appears only for transfers (any balance) and asset
  purchases (other assets only); for a return on investment it is labelled
  **Investment** (other assets only).
- **Category** lists only categories of the selected type (expense categories for
  asset purchases, income categories for returns on investment) and is hidden for transfers.
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
  debt above the limit, and negative amounts are red. There is also a
  per-currency totals table.
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
- **Delete** of a category still used by transactions shows the 409 message
  on the list.

User data is only inserted with `textContent` / input `.value` (never
`innerHTML`), so HTML in notes or names is shown as text.
