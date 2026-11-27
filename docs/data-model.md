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
| type          | `type`         | `text NOT NULL DEFAULT 'expense'`: `expense`, `income`, `transfer`, `asset_purchase`, `return_on_investment`, `asset_sale`, `repay_debt`, `balance_adjustment` (created by the server only, then editable and deletable) or `beginning_balance` (one per balance, created with it; editable, not deletable); see "Transaction types" |
| adj. direction | `adjustment_direction` | `text`, `increase` / `decrease` on `balance_adjustment` and `beginning_balance` rows, `NULL` (JSON `null`) otherwise |
| payment acct  | `balance_uid`  | `uuid` of of the balance paid from (expense), received into (income, return_on_investment, asset_sale) or moved from (transfer, asset_purchase, repay_debt); no FK |
| destination   | `to_balance_uid` | `uuid`, transfers, asset purchases, returns on investment, asset sales and debt repayments only (else `null`); UUID of the destination balance (for a return on investment, the linked `other_asset`; for an asset sale, the `other_asset` sold; for a debt repayment, the `credit_card` / `other_liability` repaid); no FK |
| dest. name    | `to_account`   | denormalized destination name snapshot (transfers, asset purchases, returns on investment, asset sales and debt repayments only, else `null`)    |
| category      | `category_uid` | `uuid`, **required** on new or edited expense, income, asset purchase and return on investment transactions (older rows may be `null`); UUID of a category of the same type (expense categories for asset purchases, income categories for returns on investment); never set for transfers; optional on a debt repayment that has interest or fees (an expense category for those charges) and on an asset sale with a gain or loss (an income category for a gain, an expense category for a loss), else not set; no FK. **The only link to the category**: no name is stored on the transaction |
| account name  | `account`      | denormalized balance `name` snapshot at write time (read-only in API)   |
| spend time    | `spent_at`     | `text` RFC 3339 with offset as entered + `spent_at_utc timestamptz` (same instant) |
| note          | `note`         | `text`, optional, up to 1000 chars                                     |
| created       | `created_at`   | `timestamptz` (server clock); JSON RFC 3339 in UTC                     |
| received amt  | `to_amount`, `to_amount_minor` | `to_amount_minor bigint` + `to_amount_scale smallint`; cross-currency transfers only (else `NULL`/`null`) |
| dest. currency | `to_currency` | `char(3)`, the destination balance's currency; cross-currency transfers only |
| rate          | `exchange_rate`, `rate_source`, `rate_date` | `text` (exact decimal, never a float): destination units per 1 source unit (≤ 10 significant digits); `text` `frankfurter`/`manual`; `date` of the ECB rate (`YYYY-MM-DD` in JSON) (Frankfurter only). Immutable history |
| foreign amt   | `foreign_amount`, `foreign_amount_minor` | `foreign_amount_minor bigint` + `foreign_amount_scale smallint`; optional, expense / income / asset_purchase / return_on_investment only (else `NULL`/`null`). **Reference only:** the amount in another currency (e.g. the USD price of a purchase charged in THB); never moves a balance and is not in any total. See "Foreign currency amount" |
| foreign cur.  | `foreign_currency` | `char(3)`, ISO 4217 code of `foreign_amount`; set together with it, never equal to `currency` |
| interest      | `interest_amount`, `interest_amount_minor` | `interest_amount_minor bigint` (at `amount_scale`, in `currency`); optional, repay_debt only (else `NULL`/`null`). Paid from the account on top of the principal. See "Debt repayment interest and fees" |
| fees          | `fee_amount`, `fee_amount_minor` | `fee_amount_minor bigint` (at `amount_scale`, in `currency`); optional, repay_debt only. Other fees paid with the repayment |
| gain / loss   | `gain_loss`, `gain_loss_minor` | `gain_loss_minor bigint` (at `amount_scale`, in `currency`; positive = gain, negative = loss); optional, asset_sale only (else `NULL`/`null`). See "Asset sale gain or loss" |
| entire sale   | `entire_asset_sale` | `boolean NOT NULL DEFAULT false`; asset_sale only: the whole asset was sold, so saving hid the asset balance |
| conversion    | `conversion_rates` | **not stored**: computed on every response for cross-currency transfers (both directions, from `amount` and `to_amount`); `null` otherwise. Read-only. See [docs/exchange-rates.md](exchange-rates.md) |
| last edited   | `updated_at`   | `timestamptz`, `NULL` (JSON `null`) until the transaction is edited; JSON RFC 3339 in UTC |

### Foreign currency amount

Expenses, income, asset purchases and returns on investment can also record
what they were worth in **another currency**, e.g. a hotel booked at 25.00
USD and charged as 850.00 THB to a THB card:

```json
{"type": "expense", "amount": "850.00", "currency": "THB", "balance_uid": "…",
 "category_uid": "…", "spent_at": "2026-10-06T21:06:00+08:00",
 "foreign_amount": "25.00", "foreign_currency": "USD"}
```

- `amount` / `currency` stay the amount in the balance's currency and are the
  only values used for balances and totals. `foreign_amount` is kept for
  reference and is never converted, summed or used to move a balance.
- Send `foreign_amount` and `foreign_currency` together (422 on the missing
  one). The currency must be a known ISO 4217 code other than `currency`; the
  amount is > 0 with at most that currency's minor digits (`"1500"` JPY).
- Not allowed on transfers (they use `to_amount` / `to_currency`, which do
  move the destination balance) or debt repayments (422).
- PUT is a full replace: omitting both clears a stored foreign amount.

### Debt repayment interest and fees

A debt repayment can separate the **interest** and **other fees** (late fee,
service charge, ...) it paid from the **principal**, e.g. a loan instalment of
1,155.50 THB that is 1,000.00 principal, 125.50 interest and 30.00 fees:

```json
{"type": "repay_debt", "amount": "1000", "currency": "THB",
 "balance_uid": "<payment account>", "to_balance_uid": "<loan>",
 "interest_amount": "125.50", "fee_amount": "30", "category_uid": "<expense category>",
 "spent_at": "2026-10-06T21:06:00+08:00"}
```

- `amount` is the **principal**: the debt goes down by it only. The payment
  account pays `amount + interest_amount + fee_amount` (1,155.50 above).
- Both are optional decimals of 0 or more in the transaction currency, with
  at most its minor digits; empty or 0 means none (stored as `null`). Not
  allowed on any other type (422).
- The interest and fees are **expenses**: list totals and the income &
  expense report count them as spent; the principal is still not counted.
- A repayment may have a `category_uid` only when it has interest or fees
  (422 otherwise). It must be an `expense` category, and the charges are
  reported under it (under "no category" when there is none).
- PUT is a full replace: omitting them clears them (and then a category must
  be omitted too).

### Asset sale gain or loss

An asset sale records selling all or part of an `other_asset`, e.g. gold
bought for 500.00 THB sold for 465.00 THB (a loss of 35.00):

```json
{"type": "asset_sale", "amount": "500", "currency": "THB",
 "balance_uid": "<account receiving the money>", "to_balance_uid": "<the other_asset>",
 "gain_loss": "-35", "category_uid": "<expense category>", "entire_asset_sale": true,
 "spent_at": "2026-10-06T21:06:00+08:00"}
```

- `amount` is the **principal**: the asset's balance goes down by it (what
  the sold part was worth in the books). The receiving account gets the
  proceeds, `amount + gain_loss` (465.00 above).
- `gain_loss` is optional, a signed decimal in the transaction currency with
  at most its minor digits: positive for a **gain**, negative for a **loss**;
  empty or 0 means neither (stored as `null`). A loss may not be larger than
  the principal (the proceeds can't be negative; 422). Not allowed on any
  other type (422).
- The gain is **income** and the loss an **expense**: list totals and the
  income & expense report count them; the principal is not counted. Together
  with returns on investment this gives the result of an investment.
- A sale may have a `category_uid` only when it has a gain or loss (422
  otherwise): an `income` category for a gain, an `expense` category for a
  loss. The gain or loss is reported under it ("no category" when there is
  none).
- `entire_asset_sale: true` records that the whole asset was sold: saving the
  transaction (create, PUT or bulk import) **hides** the asset balance, as
  `POST /api/balances/:uid/hide` does. It is not shown again automatically
  when the flag is cleared or the transaction deleted; use
  `POST /api/balances/:uid/unhide`. Not allowed on any other type (422).
- PUT is a full replace: omitting `gain_loss` clears it (and then a category
  must be omitted too); omitting `entire_asset_sale` means `false`.

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
- a **`name`** (required, up to 100 characters; need not be unique)
- a **base `currency`** (ISO 4217)
- an optional **`description`** (up to 1000 characters)
- **`hidden`**: `false` unless hidden with `POST /api/balances/:uid/hide`

The name is a short label like "Wallet". Transactions identify the payment
account by the balance's immutable **`uid`** (`balance_uid` on the transaction).
There is **no foreign key**, but deleting a balance also deletes every
transaction that references it, so none is left pointing at a missing balance
(older versions deleted only the balance; such leftovers keep their
`balance_uid` and `account` name snapshot). A balance you want to keep the
history of can be **hidden** instead (`hidden: true`). See
[balances.md §5](balances.md#5-hiding-and-deleting-a-balance).

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
is recorded as a `balance_adjustment` transaction (editable and deletable
afterwards; editing or deleting it moves the balance). Every balance and
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
- Names are **not unique**: a user may have two balances called "Cash" (for
  example in two currencies). Transactions refer to balances by `uid`; the
  name is only a label (copied into `account` / `to_account` when a
  transaction is saved). Lists sort by type, then name, then creation order.
  Schema v1 had a unique index on `lower(name)`; v2 drops it.

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

- `category_uid` is **required** on expense, income, asset purchase and
  return on investment transactions (422 on `category_uid`, e.g. "is required
  for an expense", when missing, `null` or `""`). An expense or asset purchase
  must use an `expense` category, income or a return on investment an
  `income` category (422 on `category_uid` otherwise, also for unknown or
  malformed uids, or a uid of another user's category). A transfer must not
  have a category (422). A debt repayment may have an `expense` category
  only when it has interest or fees (it is the category of those charges);
  otherwise not (422). An asset sale may have a category only when it has a
  gain (`income` category) or loss (`expense` category). Balance adjustments
  never have one.
- Transactions saved before categories were required may have no category.
  They are listed as before; editing one requires choosing a category.
- The transaction stores **only `category_uid`** (no name copy, unlike
  `account`). Clients look the name up from `GET /api/categories` (the web
  page does this), so renaming a category is reflected everywhere at once.
- **Deleting** a category that any transaction references is rejected with
  **409**; clear or change `category_uid` on those transactions first.
- **Changing a category's type** is rejected with **409** while any
  transaction references it (those transactions would no longer match); an
  unreferenced category may change type freely. Renaming is always allowed.
- A transaction PUT is a full replace, so it must send `category_uid` too
  (except for a transfer). When changing `type`, send a category of the new
  type; changing to a transfer, omit it.

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
| `asset_sale` ("Asset Sale") | like income: account `balance += amount + gain_loss`; card `debt −= amount + gain_loss` | the `other_asset` sold: `balance −= amount` (the principal); hidden when `entire_asset_sale` |
| `repay_debt` ("Repay Debt") | the `payment_account` paid from: `balance −= amount + interest_amount + fee_amount` | the `credit_card` / `other_liability` repaid: `debt −= amount` (may go negative = in credit) |
| `balance_adjustment` ("Balance Adjustment") | any balance: `increase` raises an asset's `balance` or a liability's `debt` by `amount`, `decrease` lowers it. Recorded by a manual balance edit that already set the value, so this applies only when the adjustment is edited or deleted | n/a |
| `beginning_balance` ("Beginning Balance") | its own balance: like a balance adjustment from zero (`decrease` = the balance started below zero; `amount` may be 0). Recorded with the balance, which already holds the amount, so this applies only when it is edited | n/a |

**Balance adjustments** are created only by the server, in the same database
transaction as a manual `PUT /api/balances/:uid` that changes the
`balance` (assets) or `debt` (liabilities):
- `amount` = the absolute difference; `adjustment_direction` = `increase` or
  `decrease` of that balance/debt; `currency` = the balance's;
  `spent_at` = the server's local time of the edit (with its offset); a
  generated `note` such as `Manual edit of debt: 100.00 → 175.00`; no
  category or destination.
- Limit-only changes, no-op edits and currency changes record nothing.
- Recording one does not move the balance again (the edit set it); it has
  `"adjusts_balances": true` because its change is in the balance.
- They cannot be created through the API: POST, or a PUT turning another
  transaction into one, with `"type":"balance_adjustment"` → 422.
- An existing one can be edited with PUT (`amount`, `adjustment_direction`
  — required —, `balance_uid` of any balance type, `currency`, `spent_at`,
  `note`; it stays a `balance_adjustment`) and deleted with DELETE. The
  balance moves by the difference, or back by the whole change on delete.

**Beginning balances** (issue #172) are created only by the server, one per
balance, in the same database transaction as `POST /api/balances`: the
starting `balance` / `debt` as `amount` (≥ 0) and `adjustment_direction`
(`decrease` when it is below zero), dated at the time of creation. They can
be edited like a balance adjustment except that `balance_uid` cannot change,
and they cannot be deleted (409 `beginning_balance_undeletable`; they go
with their balance). A beginning balance is always its balance's earliest
transaction: saving a transaction dated before it moves it back to that
date and time. See [balances.md §2a](balances.md#2a-beginning-balance).

Details (updates, deletes, older transactions, currency rule) are in
[docs/balances.md](balances.md). The table below says which balance
types each transaction type accepts:

| `type`     | `balance_uid` must be                         | `to_balance_uid` |
|------------|-----------------------------------------------|------------------|
| `expense`  | `payment_account` or `credit_card` (spent from) | not allowed (422) |
| `income`   | `payment_account` or `credit_card` (received into / cash rebate) | not allowed (422) |
| `transfer` | any balance type (source)                     | **required**; any existing balance type, different from `balance_uid` |
| `asset_purchase` | `payment_account` or `credit_card` (paid from) | **required**; an `other_asset` balance (422 for any other type), different from `balance_uid` |
| `return_on_investment` | `payment_account` or `credit_card` (received into) | **required**; the `other_asset` the return came from (422 for any other type), different from `balance_uid`; its value is not changed |
| `asset_sale` | `payment_account` or `credit_card` (received into) | **required**; the `other_asset` sold (422 for any other type), different from `balance_uid` |
| `repay_debt` | `payment_account` (paid from) | **required**; a `credit_card` or `other_liability` balance (422 for any other type), different from `balance_uid` |

- **Asset Purchase** (`asset_purchase`) records buying something you track as
  an `other_asset` (e.g. a laptop, gold): the source is affected like an
  expense, the asset's balance rises by the same amount. It is a value move
  like a transfer, so list totals count it as neither expense nor income. An
  **expense** category is required (e.g. Electronics); income categories are
  rejected (422).

- **Return on Investment** (`return_on_investment`) records investment income
  such as a dividend or interest payment. It behaves like income: the
  receiving `payment_account` or `credit_card` goes up (a card's debt goes
  down, and may go below zero = in credit). It must link the `other_asset`
  it came from in `to_balance_uid`, but that asset's value is **not**
  changed: the return is paid out, not withdrawn from the asset, and it is
  not an unrealized gain. `other_asset` and `other_liability` cannot receive
  it (422). An **income** category is required (e.g. Dividends);
  expense categories are rejected (422). `to_amount` / `to_currency` are not
  allowed. List totals count it as income.

- **Asset Sale** (`asset_sale`) records selling all or part of an
  `other_asset`: the asset's balance goes down by the principal (`amount`)
  and the receiving `payment_account` or `credit_card` goes up by the
  proceeds, `amount + gain_loss`. The principal moves value like a transfer;
  only the optional **gain or loss** is counted, as income or expense, with an
  optional category (see "Asset sale gain or loss"). With
  `entire_asset_sale` the asset is hidden. Both balances must be in the
  transaction's currency; `to_amount` / `to_currency` and a foreign amount
  are not allowed.

- **Repay Debt** (`repay_debt`) records paying down a debt: money leaves a
  `payment_account` and the debt of a `credit_card` (paying the card bill) or
  an `other_liability` (a loan instalment) goes down by the same amount; an
  overpayment leaves it in credit (negative debt). The principal moves value
  like a transfer, so list totals count it as neither expense nor income.
  Optional **interest and fees** (`interest_amount`, `fee_amount`) are paid
  from the account on top and counted as expenses, with an optional expense
  category (see "Debt repayment interest and fees"). Both balances must be in
  the transaction's currency (`to_amount` / `to_currency` are not allowed);
  use a transfer for a cross-currency repayment.

- Omitting `type` on POST or PUT means `expense`, so older clients keep working.
  A PUT is a full replace, so a PUT of a transfer must send `"type":"transfer"`.
- Why: income lands in a spendable account, an asset (salary, dividends), or
  a credit card as a cash rebate / statement credit (which lowers its debt;
  `other_liability` stays disallowed for income). Paying a card or loan from
  a payment account is a **repay_debt** (a transfer still works too, e.g.
  for a cross-currency repayment or paying one liability from another). Transfers may also move money into an
  `other_asset` (savings) or draw from a credit card / loan.
- The transaction's `currency` must equal the currency of its source balance
  (and, for an asset purchase, an asset sale or a debt repayment, of its destination too; the asset linked by a
  return on investment may be in any currency, since it is not moved); 422 otherwise.
- A **transfer** may go between balances in different currencies: `amount` /
  `currency` are the source side; `to_amount` / `to_currency` (destination
  currency), `exchange_rate`, `rate_source` (`frankfurter` | `manual`) and
  `rate_date` record the conversion. Send `to_amount` for a manual rate, or
  omit it and the server converts with the Frankfurter (ECB) rate of the
  `spent_at` date. Same-currency transfers: `to_amount` must be omitted or
  equal `amount`. See [docs/exchange-rates.md](exchange-rates.md).
- A PUT changing `type` away from `transfer` / `asset_purchase` / `return_on_investment` / `asset_sale` / `repay_debt` must omit `to_balance_uid` (or
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
- **Encryption at rest** (optional, v10): with `EXPENSE_ENCRYPTION_KEY` set,
  transaction notes and account names, balance names and descriptions, the
  category list and display names are stored AES-256-GCM encrypted
  (`enc1:<key id>:…`); amounts, dates and ids stay in clear. See
  [Encryption at rest](encryption.md).
- **Database from a newer build:** startup fails with `unsupported database
  schema` and the database is not modified.
- **Concurrency:** writes run on a small connection pool. Writes that read and
  then change the same rows lock them (`SELECT … FOR UPDATE`; balances are
  locked in a fixed order, so concurrent transfers can't deadlock), and
  category writes are serialized per user.

| Version | Change |
|---------|--------|
| 1       | baseline: `users`, `user_identities`, `sessions`, `api_tokens`, `user_categories`, `balances`, `transactions`, `fx_rates`, `fx_rate_lookups` and their indexes |
| 2       | balance names no longer unique: drops `balances_user_name_uq` |
| 3       | transaction type `repay_debt` allowed: widens `transactions_type_check` |
| 4       | optional foreign-currency amount: adds `transactions.foreign_amount_minor`, `foreign_amount_scale`, `foreign_currency` and `transactions_foreign_amount_check` |
| 5       | optional interest and fees on debt repayments: adds `transactions.interest_amount_minor`, `fee_amount_minor`, `transactions_repay_charges_check` and `transactions_repay_category_check` |
| 6       | balance adjustments editable and deletable: sets `balance_applied = true` on `balance_adjustment` rows, replaces the baseline check that required it false with `transactions_adjustment_check` (no destination or category) |
| 7       | balances can be hidden: adds `balances.hidden boolean NOT NULL DEFAULT false` |
| 8       | transaction type `asset_sale`: widens `transactions_type_check`, adds `transactions.gain_loss_minor`, `entire_asset_sale`, `transactions_sale_result_check`, `transactions_sale_proceeds_check` and `transactions_sale_category_check` |
| 9       | transaction type `beginning_balance`: widens `transactions_type_check`, lets `transactions_amount_minor_check` accept 0 on it, replaces the baseline direction check with `transactions_direction_check`, adds `transactions_beginning_balance_check` (no destination, category or import ref) and the unique index `transactions_beginning_balance_uq` (one per balance), and records one for every existing balance: its current amount minus what its transactions did, dated at its earliest transaction (or its creation) |
| 10      | encryption at rest ([encryption.md](encryption.md)): adds the table `encryption_state` (one row once the database is encrypted: key id and a check value) and lets `user_categories_categories_check` accept a jsonb string (the encrypted list) as well as an array. Encrypting the existing values happens at startup with a key, not in the migration |

**Coming from SQLite?** Earlier versions stored everything in
`data/expenses.db`. That data is **not migrated**: the Postgres version starts
fresh. The old file is simply no longer read; keep it as a backup or delete it
(with `expenses.db-wal` / `expenses.db-shm`).

To change the schema, append a migration to `migrations` in
`pkg/store/migrate.go` (it becomes v2, then v3, ...) instead of editing the
baseline; the steps are documented at the top of that file. A fresh database
runs every step from 0, so it always matches an upgraded one.
