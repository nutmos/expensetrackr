# Data model

[← Back to the README](../README.md) · [All docs](README.md)

Transactions and their fields, transaction types, balances, categories,
users, amounts, dates and the database schema.

Transactions are stored in the Postgres table **`transactions`**, with index
`transactions_user_spent_idx` on `(user_uid, spent_at_utc DESC, id DESC)` (see
"Database schema"). The HTTP API uses `/api/transactions` and the list key
`transactions` (the old `/api/transactions` paths were removed, no alias).

| Field         | JSON           | Stored as                                                              |
|---------------|----------------|------------------------------------------------------------------------|
| id            | (not exposed)  | `bigint` identity, internal only; the API uses `uid`                    |
| uid           | `uid`          | `uuid NOT NULL UNIQUE`, server-assigned UUID v4 (lowercase), immutable; a `uid` in PUT bodies is ignored |
| amount        | `amount`       | `amount_minor bigint` + `amount_scale smallint` (see below)             |
| amount (raw)  | `amount_minor` | the integer itself, e.g. `12050`                                       |
| currency      | `currency`     | `char(3)`, ISO 4217 alphabetic code, upper-cased (`THB`, `SGD`, `USD`, …) |
| type          | `type`         | `text NOT NULL DEFAULT 'expense'`: `expense`, `income`, `transfer`, `asset_purchase`, `return_on_investment`, or `balance_adjustment` (server-created only; see "Transaction types") |
| adj. direction | `adjustment_direction` | `text`, `increase` / `decrease` on `balance_adjustment` rows, `NULL` (JSON `null`) otherwise |
| payment acct  | `balance_uid`  | `uuid` of of the balance paid from (expense), received into (income, return_on_investment) or moved from (transfer, asset_purchase); no FK |
| destination   | `to_balance_uid` | `uuid`, transfers, asset purchases and returns on investment only (else `null`); UUID of the destination balance (for a return on investment, the linked `other_asset`); no FK |
| dest. name    | `to_account`   | denormalized destination name snapshot (transfers, asset purchases and returns on investment only, else `null`)    |
| category      | `category_uid` | `uuid`, optional (`null` = none); UUID of a category of the same type (expense categories for asset purchases, income categories for returns on investment); never set for transfers; no FK. **The only link to the category**: no name is stored on the transaction |
| account name  | `account`      | denormalized balance `name` snapshot at write time (read-only in API)   |
| spend time    | `spent_at`     | `text` RFC 3339 with offset as entered + `spent_at_utc timestamptz` (same instant) |
| note          | `note`         | `text`, optional, up to 1000 chars                                     |
| created       | `created_at`   | `timestamptz` (server clock); JSON RFC 3339 in UTC                     |
| received amt  | `to_amount`, `to_amount_minor` | `to_amount_minor bigint` + `to_amount_scale smallint`; cross-currency transfers only (else `NULL`/`null`) |
| dest. currency | `to_currency` | `char(3)`, the destination balance's currency; cross-currency transfers only |
| rate          | `exchange_rate`, `rate_source`, `rate_date` | `text` (exact decimal, never a float): destination units per 1 source unit (≤ 10 significant digits); `text` `frankfurter`/`manual`; `date` of the ECB rate (`YYYY-MM-DD` in JSON) (Frankfurter only). Immutable history |
| conversion    | `conversion_rates` | **not stored**: computed on every response for cross-currency transfers (both directions, from `amount` and `to_amount`); `null` otherwise. Read-only. See [docs/exchange-rates.md](exchange-rates.md) |
| last edited   | `updated_at`   | `timestamptz`, `NULL` (JSON `null`) until the transaction is edited; JSON RFC 3339 in UTC |

## Balances (table `balances`)

A **balance** records the current state of an account. There are four types:

| Type              | Kind      | Amount fields      | Rules |
|-------------------|-----------|--------------------|-------|
| `payment_account` | asset     | `balance`          | required; may be **negative** (e.g. an overdrawn account) or zero |
| `other_asset`     | asset     | `balance`          | same as above |
| `credit_card`     | liability | `debt`, `limit`    | both required; limit ≥ 0. Debt may be **negative** (overpaid, in credit) and **may exceed** the limit (flagged `over_limit: true`) |
| `other_liability` | liability | `debt`, `limit`    | same as above (for a loan, `limit` could be the credit line or the original principal; it's up to you) |

The **type is fixed at creation**: a `credit_card` can never become a
`payment_account`, etc. PUT still requires `type` and accepts
it only when it equals the stored type (case-insensitive), so sending back a
GET response works. A different type is rejected with **422** and
`"fields":{"type":"balance type cannot be changed after creation"}`. Other
fields are still checked against the stored type in the same response, and
nothing is changed. To change the type, create a new balance and delete the
old one.

Every balance also has:

- a **`uid`**: server-assigned UUID v4 (`xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx`,
  lowercase), unique and **immutable**. Generated on create; clients never set
  it (a `uid` in a POST/PUT body is ignored so round-tripping a previous
  response is fine). Included in every JSON response.
- a **`name`** (required, up to 100 characters, unique case-insensitively)
- a **base `currency`** (ISO 4217)
- an optional **`description`** (up to 1000 characters)

The name is a short label like "Wallet". Transactions identify the payment
account by the balance's immutable **`uid`** (`balance_uid` on the transaction).
There is **no foreign key**: deleting a balance leaves the transaction's
`balance_uid` and the denormalized `account` name snapshot intact.

**Balances adjust automatically.** Creating, editing or deleting
a transaction moves its balances in the same database transaction:
- an expense lowers an account's balance or raises a card's debt;
- income raises a balance or lowers a credit card's debt (cash rebate);
- a transfer moves value from source to destination.

The transaction currency must match the (source) balance currency (422
otherwise). A transfer between balances in different currencies is converted:
the destination moves by `to_amount` in its own currency, either sent by the
client (manual rate) or computed from the ECB rate via Frankfurter (see
[docs/exchange-rates.md](exchange-rates.md)). Manual PUT still sets amounts directly and overrides those
changes (no separate adjustments API); a change of the balance/debt amount
is recorded as a read-only `balance_adjustment` transaction. Every balance and
transaction has a `version`; PUT must send it (`If-Match` or
`"version"`), else 428, and get 409 when stale. Sign rules, manual
overrides and versioning are covered in [docs/balances.md](balances.md).

- Amounts use the same integer minor units + `amount_scale` scheme as transactions.
- Sending an amount that the type does not use returns 422. Absent, `null` or
  `""` counts as not sent.
- Fields that don't apply are `null` in responses.
- Liabilities also return the derived `available` = `limit − debt` (negative
  when over the limit) and `over_limit`.
- A CHECK constraint enforces the per-type columns in the database too.
- Names are unique per user **case-insensitively** (a unique index on
  `lower(name)`, which folds every script that has case, not only ASCII;
  Thai has no case).

## Categories (per user, table `user_categories`)

A **category** classifies expenses and income. Fields: server-assigned
immutable `uid` (UUID v4), `name` (required, ≤ 100 chars), `type`
(`expense` or `income`), optional `description` (≤ 1000), `created_at`,
`updated_at`. Names are unique **case-insensitively within a type**
(`Food` as expense and `food` as income may coexist); a duplicate is 409.

Categories are **per user** (issue #34): every `/api/categories`
call works on the logged-in user's own set, and another user's category uid
is a 404. All of a user's categories are stored in **one row** of
`user_categories` as a JSON array (at most 500 per user, 409 beyond). See
[docs/categories.md](categories.md).

Transactions and balances are likewise **per user** (issue #62): every list,
get, create, update and delete is scoped to the logged-in account (cookie
session or Bearer API token). Another user's uid is a 404; a transaction may
only reference that user's own balances and categories.

Rules linking transactions and categories:

- `category_uid` is optional on expense and income transactions. An expense
  must use an `expense` category and income an `income` category (422 on
  `category_uid` otherwise, also for unknown or malformed uids, or a uid of
  another user's category). A transfer
  must not have a category (422).
- The transaction stores **only `category_uid`** (no name copy, unlike
  `account`). Clients look the name up from `GET /api/categories` (the web
  page does this), so renaming a category is reflected everywhere at once.
- **Deleting** a category that any transaction references is rejected with
  **409**; clear or change `category_uid` on those transactions first.
- **Changing a category's type** is rejected with **409** while any
  transaction references it (those transactions would no longer match); an
  unreferenced category may change type freely. Renaming is always allowed.
- A transaction PUT without `category_uid` (or with `""`) clears it; when
  changing `type`, omit the old category (it belongs to the old type) or send
  one of the new type.

## Users (tables `users`, `user_identities`)

User profiles. Each has a `uid`, a required
`display_name` and an optional `username` and `email`, both unique and
case-insensitive. `preferences` is one **JSON object** per user (a `jsonb`
column checked with `jsonb_typeof = 'object'`), modelled as a typed struct in Go. For
now it has one key, `base_currency` (default `USD`); see
[docs/user-profile.md](user-profile.md#preferences). Each user edits
their own on the **Settings** page.

There are also fields reserved for future login: `password_hash` (never
exposed; the API shows only `has_password`), `password_updated_at`,
`email_verified_at`, `last_login_at` and `status` (`active` | `disabled`).
SSO links (Sign in with Apple / Google) go in `user_identities`, unique per
`(provider, provider_subject)`.

Username/password login uses `password_hash` (bcrypt) and the `sessions`
table; see [docs/auth.md](auth.md). Existing data is still not
tied to a user. Details and follow-ups are in
[docs/user-profile.md](user-profile.md).

## Transaction types

`amount` is always positive; `type` gives the direction. Each transaction
adjusts its balances automatically:

| `type` | `balance_uid` | `to_balance_uid` |
|---|---|---|
| `expense` | asset `balance −= amount`; card `debt += amount` | n/a |
| `income` | asset `balance += amount`; card `debt −= amount` (cash rebate / credit; may go negative = in credit) | n/a |
| `transfer` | source: asset `balance −= amount`, liability `debt += amount` (drawing a loan / cash advance) | destination: asset `balance += amount`, liability `debt −= amount` (card payment / loan repayment) |
| `asset_purchase` ("Asset Purchase") | like an expense: asset `balance −= amount`; card `debt += amount` | the `other_asset` bought: `balance += amount` |
| `return_on_investment` ("Return on Investment") | like income: account `balance += amount`; card `debt −= amount` (may go negative = in credit) | the linked `other_asset`: **unchanged** (link only) |
| `balance_adjustment` ("Balance Adjustment") | none: records a manual balance edit that already set the value | n/a |

**Balance adjustments** are created only by the server, in the same database
transaction as a manual `PUT /api/balances/:uid` that changes the
`balance` (assets) or `debt` (liabilities):
- `amount` = the absolute difference; `adjustment_direction` = `increase` or
  `decrease` of that balance/debt; `currency` = the balance's;
  `spent_at` = the server's local time of the edit (with its offset); a
  generated `note` such as `Manual edit of debt: 100.00 → 175.00`; no
  category or destination.
- Limit-only changes, no-op edits and currency changes record nothing.
- They never move a balance (`"adjusts_balances": false`).
- Read-only: POST/PUT with `"type":"balance_adjustment"` → 422;
  PUT/DELETE of an existing one → 409 `balance_adjustment_readonly`.

Details (updates, deletes, older transactions, currency rule) are in
[docs/balances.md](balances.md). The table below says which balance
types each transaction type accepts:

| `type`     | `balance_uid` must be                         | `to_balance_uid` |
|------------|-----------------------------------------------|------------------|
| `expense`  | `payment_account` or `credit_card` (spent from) | not allowed (422) |
| `income`   | `payment_account`, `other_asset` or `credit_card` (received into / cash rebate) | not allowed (422) |
| `transfer` | any balance type (source)                     | **required**; any existing balance type, different from `balance_uid` |
| `asset_purchase` | `payment_account` or `credit_card` (paid from) | **required**; an `other_asset` balance (422 for any other type), different from `balance_uid` |
| `return_on_investment` | `payment_account` or `credit_card` (received into) | **required**; the `other_asset` the return came from (422 for any other type), different from `balance_uid`; its value is not changed |

- **Asset Purchase** (`asset_purchase`) records buying something you track as
  an `other_asset` (e.g. a laptop, gold): the source is affected like an
  expense, the asset's balance rises by the same amount. It is a value move
  like a transfer, so list totals count it as neither expense nor income. An
  optional **expense** category may be set (e.g. Electronics); income
  categories are rejected (422).

- **Return on Investment** (`return_on_investment`) records investment income
  such as a dividend or interest payment. It behaves like income: the
  receiving `payment_account` or `credit_card` goes up (a card's debt goes
  down, and may go below zero = in credit). It must link the `other_asset`
  it came from in `to_balance_uid`, but that asset's value is **not**
  changed: the return is paid out, not withdrawn from the asset, and it is
  not an unrealized gain. `other_asset` and `other_liability` cannot receive
  it (422). An optional **income** category may be set (e.g. Dividends);
  expense categories are rejected (422). `to_amount` / `to_currency` are not
  allowed. List totals count it as income.

- Omitting `type` on POST or PUT means `expense`, so older clients keep working.
  A PUT is a full replace, so a PUT of a transfer must send `"type":"transfer"`.
- Why: income lands in a spendable account, an asset (salary, dividends), or
  a credit card as a cash rebate / statement credit (which lowers its debt;
  `other_liability` stays disallowed for income). Paying a card or loan from
  another account is still a transfer. Transfers may also move money into an
  `other_asset` (savings) or draw from a credit card / loan.
- The transaction's `currency` must equal the currency of its source balance
  (and, for an asset purchase, of its destination too; the asset linked by a
  return on investment may be in any currency, since it is not moved); 422 otherwise.
- A **transfer** may go between balances in different currencies: `amount` /
  `currency` are the source side; `to_amount` / `to_currency` (destination
  currency), `exchange_rate`, `rate_source` (`frankfurter` | `manual`) and
  `rate_date` record the conversion. Send `to_amount` for a manual rate, or
  omit it and the server converts with the Frankfurter (ECB) rate of the
  `spent_at` date. Same-currency transfers: `to_amount` must be omitted or
  equal `amount`. See [docs/exchange-rates.md](exchange-rates.md).
- A PUT changing `type` away from `transfer` / `asset_purchase` / `return_on_investment` must omit `to_balance_uid` (or
  send `""`), else 422; changing to one of them requires `to_balance_uid`
  (or a 422).

## Amounts: integer minor units (no floats)

The amount is parsed from its **decimal text** straight into an integer count of
the currency's minor unit, with no floating point anywhere: `"120.50"` THB is
stored as `12050`, `"1500"` JPY as `1500` (JPY has no minor unit), `"1.234"` KWD
as `1234`. The number of decimals per currency comes from the ISO 4217 table in
`pkg/money/currency.go`. Input with more decimals than the currency
allows is rejected rather than rounded. Extra trailing zeros are accepted
because they carry no value (`"1500.00"` JPY is 1500, `"1.230"` USD is 1.23). `amount_scale` is stored on every row so
a row still reads correctly if the currency table changes later. The API accepts
`amount` as a JSON string (`"120.50"`, recommended) or a JSON number (`120.50`).
A number is read as its literal text, never as a float. Responses return both
`amount` (a decimal string) and `amount_minor`.

## Spend date-time: RFC 3339 with offset

`spent_at` must be a full RFC 3339 / ISO 8601 date-time **with** a UTC offset,
e.g. `2026-10-06T21:06:00+08:00` or `2026-10-06T13:06:00Z`. Values without an
offset, date-only values and other formats are rejected. The value is stored at
second precision with the **original offset kept** (so you can still see that a
Bangkok transaction was entered at +07:00). The derived `spent_at_utc`
(`timestamptz`) column holds the absolute instant and is used for ordering,
filtering and paging, so rows entered with different offsets compare correctly.

The web form has only a date-time field: it always uses the **device's current
time zone**. On create and edit the entered wall-clock time is sent with the
device's UTC offset for that date (DST-aware); there is no manual offset field.
When you edit a record saved with a different offset (e.g. `09:15+07:00` on a
`+08:00` device), the form shows it **converted to device local time**
(10:15) and saving re-stamps it with the device offset
(`10:15+08:00`, the same instant). The list also shows times in device local
time; hover a time to see the stored value. The API is unchanged and still
accepts any RFC 3339 offset.

## Database schema

The app uses **Postgres** (since the move from SQLite; see below). The whole
schema is one **baseline (version 1)**, defined in `pkg/store/schema.go`;
versioning lives in `pkg/store/migrate.go`.

- **Types:** `uuid` uids, `timestamptz` times (shown as RFC 3339 UTC in JSON),
  `jsonb` for preferences and the per-user category list, `boolean`, `date`,
  `char(3)` currencies and `bigint` identity ids. Amounts are `bigint` minor
  units; rates are exact decimal `text`. Case-insensitive uniqueness uses
  `lower(...)` indexes, and every unique constraint is named (the store maps
  violations to errors by name).
- **Versions** are recorded in the table `schema_migrations`. On start the
  server applies every pending step in **one SQL transaction** under an
  advisory lock, so servers starting together migrate once and a failed
  upgrade changes nothing. Logged as `store: migrated schema from version N to M`.
- **Database from a newer build:** startup fails with `unsupported database
  schema` and the database is not modified.
- **Concurrency:** writes run on a small connection pool. Writes that read and
  then change the same rows lock them (`SELECT … FOR UPDATE`; balances are
  locked in a fixed order, so concurrent transfers can't deadlock), and
  category writes are serialized per user.

| Version | Change |
|---------|--------|
| 1       | baseline: `users`, `user_identities`, `sessions`, `api_tokens`, `user_categories`, `balances`, `transactions`, `fx_rates`, `fx_rate_lookups` and their indexes |

**Coming from SQLite?** Earlier versions stored everything in
`data/expenses.db`. That data is **not migrated**: the Postgres version starts
fresh. The old file is simply no longer read; keep it as a backup or delete it
(with `expenses.db-wal` / `expenses.db-shm`).

To change the schema, append a migration to `migrations` in
`pkg/store/migrate.go` (it becomes v2, then v3, ...) instead of editing the
baseline; the steps are documented at the top of that file. A fresh database
runs every step from 0, so it always matches an upgraded one.
