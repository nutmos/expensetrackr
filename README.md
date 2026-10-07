# expense-service

A small expense-logging web service in Go (Gin) with a plain HTML + vanilla JS
web page and a JSON API. Data is stored in SQLite through the pure-Go
`modernc.org/sqlite` driver, so no CGO or C toolchain is needed.

Status: prototype for local use. It listens on 127.0.0.1 only and has no
authentication; see "Open questions".

## Requirements

- Go 1.24+ (`go.mod` targets go 1.24 / toolchain go1.24.4, and every dependency
  is pinned to a version that builds with the locally installed Go 1.24.4)
- No CGO (`CGO_ENABLED=0` works)

## Run

```bash
cd /workspace/expense-service
go run ./cmd/server                      # http://127.0.0.1:8080, DB at data/expenses.db

# or build a binary
CGO_ENABLED=0 go build -o bin/expense-server ./cmd/server
./bin/expense-server -addr 127.0.0.1:8080 -db data/expenses.db
```

| Flag    | Env var        | Default            | Notes                                       |
|---------|----------------|--------------------|---------------------------------------------|
| `-addr` | `EXPENSE_ADDR` | `127.0.0.1:8080`   | Keep on 127.0.0.1: there is no auth (`:8080` would expose it on all interfaces) |
| `-db`   | `EXPENSE_DB`   | `data/expenses.db` | Created automatically (directory included)  |

Set `GIN_MODE=release` to silence Gin's debug output.

Run in the background:

```bash
nohup ./bin/expense-server > server.log 2>&1 &
```

Open http://127.0.0.1:8080/ for the web page.

## Test

```bash
go vet ./...
go test ./...
```

Handler tests use a throwaway SQLite file under `t.TempDir()`, so they never
touch the real database.

## Data model

Transactions are stored in the SQLite table **`transactions`**, with index
`idx_transactions_spent_at_unix` on `(spent_at_unix DESC, id DESC)`. Until schema
version 2 the table was named `expenses`; see "Schema migrations". The HTTP API
uses `/api/transactions` and the list key `transactions` (the old
`/api/transactions` paths were removed, no alias).

| Field         | JSON           | Stored as                                                              |
|---------------|----------------|------------------------------------------------------------------------|
| id            | `id`           | `INTEGER PRIMARY KEY AUTOINCREMENT`                                    |
| uid           | `uid`          | `TEXT NOT NULL UNIQUE`, server-assigned UUID v4 (lowercase), immutable; a `uid` in PUT/PATCH bodies is ignored |
| amount        | `amount`       | `amount_minor INTEGER` + `amount_scale INTEGER` (see below)             |
| amount (raw)  | `amount_minor` | the integer itself, e.g. `12050`                                       |
| currency      | `currency`     | `TEXT`, ISO 4217 alphabetic code, upper-cased (`THB`, `SGD`, `USD`, …)  |
| payment acct  | `balance_uid`  | `TEXT` UUID of a payable balance (`payment_account` or `credit_card`); no FK |
| account name  | `account`      | denormalized balance `name` snapshot at write time (read-only in API)   |
| spend time    | `spent_at`     | `TEXT` RFC 3339 with offset as entered + `spent_at_unix INTEGER`        |
| note          | `note`         | `TEXT`, optional, up to 1000 chars                                     |
| created       | `created_at`   | `TEXT` RFC 3339 in UTC (server clock)                                  |
| last edited   | `updated_at`   | `TEXT` RFC 3339 in UTC, `NULL` (JSON `null`) until the transaction is edited |

### Balances (table `balances`)

A **balance** records the current state of an account. There are four types:

| Type              | Kind      | Amount fields      | Rules |
|-------------------|-----------|--------------------|-------|
| `payment_account` | asset     | `balance`          | required; may be **negative** (e.g. an overdrawn account) or zero |
| `other_asset`     | asset     | `balance`          | same as above |
| `credit_card`     | liability | `debt`, `limit`    | both required, both ≥ 0; debt **may exceed** the limit (flagged `over_limit: true`) |
| `other_liability` | liability | `debt`, `limit`    | same as above (for a loan, `limit` could be the credit line or the original principal; it's up to you) |

Every balance also has:

- a **`uid`**: server-assigned UUID v4 (`xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx`,
  lowercase), unique and **immutable**. Generated on create; clients never set
  it (a `uid` in a POST/PUT/PATCH body is ignored so round-tripping a previous
  response is fine). Included in every JSON response.
- a **`name`** (required, up to 100 characters, unique case-insensitively)
- a **base `currency`** (ISO 4217)
- an optional **`description`** (up to 1000 characters)

The name is a short label like "KBank debit". Transactions identify the payment
account by the balance's immutable **`uid`** (`balance_uid` on the transaction).
There is **no SQLite foreign key**: deleting a balance leaves the transaction's
`balance_uid` and the denormalized `account` name snapshot intact. Logging an
transaction does **not** yet adjust the balance amount (see Open questions).

- Amounts use the same integer minor units + `amount_scale` scheme as transactions.
- Sending an amount that the type does not use returns 422. Absent, `null` or
  `""` counts as not sent.
- Fields that don't apply are `null` in responses.
- Liabilities also return the derived `available` = `limit − debt` (negative
  when over the limit) and `over_limit`.
- A CHECK constraint enforces the per-type columns in the database too.
- Uniqueness uses SQLite `NOCASE`, which ignores case for ASCII letters only.
  Thai names have no case, so this is enough.

### Amounts: integer minor units (no floats)

The amount is parsed from its **decimal text** straight into an integer count of
the currency's minor unit, with no floating point anywhere: `"120.50"` THB is
stored as `12050`, `"1500"` JPY as `1500` (JPY has no minor unit), `"1.234"` KWD
as `1234`. The number of decimals per currency comes from the ISO 4217 table in
`internal/transaction/currency.go`. Input with more decimals than the currency
allows is rejected rather than rounded. Extra trailing zeros are accepted
because they carry no value (`"1500.00"` JPY is 1500, `"1.230"` USD is 1.23). `amount_scale` is stored on every row so
a row still reads correctly if the currency table changes later. The API accepts
`amount` as a JSON string (`"120.50"`, recommended) or a JSON number (`120.50`).
A number is read as its literal text, never as a float. Responses return both
`amount` (a decimal string) and `amount_minor`.

### Spend date-time: RFC 3339 with offset

`spent_at` must be a full RFC 3339 / ISO 8601 date-time **with** a UTC offset,
e.g. `2026-10-06T21:06:00+08:00` or `2026-10-06T13:06:00Z`. Values without an
offset, date-only values and other formats are rejected. The value is stored at
second precision with the **original offset kept** (so you can still see that a
Bangkok transaction was entered at +07:00). The derived `spent_at_unix` column holds
the absolute instant and is used for ordering and filtering, so rows entered with
different offsets compare correctly.

The web form has a date-time field plus a separate UTC-offset field. For a new
transaction both default to the browser's current local time and offset, and the
offset follows the chosen date (DST-aware) until you type your own (for example
`+07:00` for a purchase in Bangkok). When you edit a transaction, the form shows the
saved wall-clock time **in its original offset**, so a transaction saved as
`09:15+07:00` appears as 09:15 with `+07:00` and is not converted to your
browser's zone.

### Schema migrations

The schema version is tracked in SQLite's `PRAGMA user_version`. The code is in
`internal/store/migrate.go`.

- **New, empty database:** the latest schema (`transactions` and `balances`
  tables + indexes) is created directly at the current version, in one SQL
  transaction.
- **Existing database:** pending migrations run in order. Each migration and
  its `user_version` bump share one SQL transaction, so an interrupted upgrade
  rolls back to the previous version and runs again on the next start.
  Migrations are also written to be idempotent, as a second line of defence.

| Version | Change |
|---------|--------|
| 0       | original schema: table `expenses`, index `idx_expenses_spent_at_unix` |
| 1       | `ALTER TABLE expenses ADD COLUMN updated_at TEXT` (NULL for existing rows) |
| 2       | `ALTER TABLE expenses RENAME TO transactions`, then `DROP INDEX IF EXISTS idx_expenses_spent_at_unix` and `CREATE INDEX IF NOT EXISTS idx_transactions_spent_at_unix ON transactions (spent_at_unix DESC, id DESC)` |
| 3       | `CREATE TABLE IF NOT EXISTS balances (...)` with per-type CHECK constraints and `name COLLATE NOCASE UNIQUE`, plus `CREATE INDEX IF NOT EXISTS idx_balances_type ON balances (type, name)`. `transactions` is not touched. |
| 4       | `ALTER TABLE balances ADD COLUMN uid TEXT` (if missing), backfill every empty uid with a new UUID v4, then `CREATE UNIQUE INDEX IF NOT EXISTS idx_balances_uid ON balances (uid)`. `transactions` is not touched. New databases create `uid TEXT NOT NULL UNIQUE` from the start. |
| 5       | `ALTER TABLE transactions ADD COLUMN balance_uid TEXT` (if missing); match each free-text `account` to a payable balance name (`payment_account` / `credit_card`, case-insensitive) and set `balance_uid`; fail the migration if any row cannot be matched; `CREATE INDEX IF NOT EXISTS idx_transactions_balance_uid ON transactions (balance_uid)`. The `account` column is kept as a denormalized name snapshot. New databases create `balance_uid TEXT NOT NULL` (+ length CHECK) from the start. |
| 6       | `ALTER TABLE transactions ADD COLUMN uid TEXT` (if missing), backfill every empty uid with a new UUID v4, then `CREATE UNIQUE INDEX IF NOT EXISTS idx_transactions_uid ON transactions (uid)`. Same style as v4; idempotent (existing uids kept). New databases create `uid TEXT NOT NULL UNIQUE` from the start. |

Notes on version 2:

- `RENAME TO` keeps every row, the AUTOINCREMENT counter (deleted IDs are still
  not reused) and attached indexes.
- SQLite cannot rename an index, so the old one is dropped and recreated
  under the new name.
- If the table is already called `transactions` (e.g. renamed by hand), only
  the index is fixed.
- If **both** `expenses` and `transactions` exist, startup fails with a clear
  error and nothing is changed. The tool won't guess which table holds the data.

Version 3 is idempotent thanks to `IF NOT EXISTS`. If a `balances` table
already exists without the expected columns, startup fails and nothing is
changed. Version 4 is idempotent: existing uids are kept, only empty ones are
filled, and the unique index is created with `IF NOT EXISTS`.

Version 5 notes:

- Payable types only: `payment_account` and `credit_card`. `other_asset` /
  `other_liability` cannot be used as a payment account (they are not spendable
  wallets in this prototype).
- Matching is by balance `name` (`COLLATE NOCASE`) against the old free-text
  `account` value. An empty `transactions` table migrates cleanly. If any row
  cannot be matched, startup fails with a count and nothing is committed —
  create the missing payable balances (or delete the orphan rows) and restart.
- No foreign key is added. The `account` column remains as a display snapshot
  written at create/update time from the balance's current name.

Version 1 checks `pragma_table_info` before adding the column. A database with a
newer version than the binary supports is refused rather than modified. Each
step is logged (`store: migrated schema to version N`, or `store: created new
database at schema version N`). To be extra safe, stop the server and copy
`data/expenses.db` (the file name is unchanged) to `data/backups/` before
upgrading.

## API

All responses are JSON. Every error looks like
`{"error": "message", "fields": {"field": "problem", ...}}`. `fields` appears
only on validation and duplicate-name errors.

| Method | Path                 | Success | Notes |
|--------|----------------------|---------|-------|
| POST   | `/api/transactions`      | 201 + transaction, `Location` header | 422 on validation errors, 400 on malformed JSON or unknown fields |
| GET    | `/api/transactions`      | 200 `{"transactions":[...],"count":n}` | Newest first by spend time. Optional `from`, `to` (RFC 3339 with offset, both inclusive), `limit` (1–5000, default 500) |
| GET    | `/api/transactions/:id`  | 200 + transaction | `:id` may be the numeric id **or** the transaction `uid` (all digits = id, otherwise uid; same for PUT/PATCH/DELETE). 404 if missing, 400 for a non-positive numeric id |
| PUT    | `/api/transactions/:id`  | 200 + updated transaction | Full replace with the same body and rules as POST. An omitted `note` clears it. 404 if missing, 422 for invalid values, 400 for malformed JSON or unknown fields |
| PATCH  | `/api/transactions/:id`  | 200 + updated transaction | Partial update: only the fields you send change, then the merged result is validated like a create. `"note": null` clears the note; `null` for any other field returns 422. Returns 400 for `{}`, unknown fields or wrong JSON types, and 404 if missing |
| DELETE | `/api/transactions/:id`  | 204 | 404 if missing |
| POST   | `/api/balances`      | 201 + balance, `Location` header | 422 on validation errors (incl. per-type amount rules), 409 if the name is already used (case-insensitive), 400 on malformed JSON or unknown fields |
| GET    | `/api/balances`      | 200 `{"balances":[...],"count":n,"totals":[...]}` | Grouped by type, then by name. Optional `type=` filter (400 if invalid). Optional `payable=1` / `payable=true` returns only `payment_account` and `credit_card` (cannot combine with `type=`). `totals` covers the returned rows, per currency |
| GET    | `/api/balances/:id`  | 200 + balance | `:id` may be the numeric id **or** the UUID `uid` (all-digit paths are treated as numeric ids; anything else as uid). 404 if missing |
| PUT    | `/api/balances/:id`  | 200 + updated balance | Full replace (id or uid). A type change must include the new type's amounts and leave out the old ones. `uid` in the body is ignored. Returns 404, 409, 422 or 400 as for POST |
| PATCH  | `/api/balances/:id`  | 200 + updated balance | Partial update (id or uid). When `type` changes, amounts the new type doesn't use are dropped automatically and the new type's amounts must be in the same PATCH. `"description": null` clears it; `"balance"`/`"debt"`/`"limit": null` removes that amount. A currency change re-reads the amounts using the new currency's decimals. `uid` in the body is ignored |
| DELETE | `/api/balances/:id`  | 204 | id or uid; 404 if missing |
| GET    | `/api/healthz`       | 200 `{"status":"ok"}` | |
| GET    | `/`                  | HTML page | Static assets under `/static/` |

### Examples

Create a payable balance first (or use an existing `uid`), then log a transaction
with `balance_uid`. The free-text `account` field is **not** accepted on write
(unknown field → 400); responses still include `account` as the name snapshot.

```bash
BAL_UID=$(curl -s -X POST http://127.0.0.1:8080/api/balances \
  -H 'Content-Type: application/json' \
  -d '{"name":"KBank debit","type":"payment_account","currency":"THB","balance":"10000"}' \
  | python3 -c 'import sys,json; print(json.load(sys.stdin)["uid"])')

curl -s -X POST http://127.0.0.1:8080/api/transactions \
  -H 'Content-Type: application/json' \
  -d "{\"amount\":\"120.50\",\"currency\":\"THB\",\"balance_uid\":\"$BAL_UID\",\"spent_at\":\"2026-10-06T21:06:00+08:00\",\"note\":\"Lunch\"}"
```

```json
{"id":1,"uid":"5f0c2e9a-7b1d-4c3e-9a8f-2d6b1e4c7a90","amount":"120.50","amount_minor":12050,"currency":"THB",
 "balance_uid":"a1b2c3d4-e5f6-4789-a012-3456789abcde","account":"KBank debit",
 "spent_at":"2026-10-06T21:06:00+08:00","note":"Lunch","created_at":"2026-10-06T13:20:11Z"}
```

Unknown or non-payable `balance_uid` → HTTP 422 on that field:

```json
{"error":"validation failed","fields":{"balance_uid":"does not match any balance"}}
```

Validation error (HTTP 422):

```json
{"error":"validation failed","fields":{
  "currency":"\"XYZ\" is not a recognised ISO 4217 currency code",
  "spent_at":"must be an RFC 3339 / ISO 8601 date-time with a timezone offset, e.g. 2026-10-06T21:06:00+08:00"}}
```

Edit: full replace (PUT) and partial update (PATCH). Both keep `id` and
`created_at` and set `updated_at`:

```bash
curl -s -X PUT http://127.0.0.1:8080/api/transactions/1 -H 'Content-Type: application/json' \
  -d "{\"amount\":\"150\",\"currency\":\"THB\",\"balance_uid\":\"$BAL_UID\",\"spent_at\":\"2026-10-05T09:15:00+07:00\",\"note\":\"pad thai + drink\"}"
# 200 {"id":1,"amount":"150.00","amount_minor":15000,"currency":"THB",...,"updated_at":"2026-10-06T13:18:11Z"}

curl -s -X PATCH http://127.0.0.1:8080/api/transactions/2 -H 'Content-Type: application/json' \
  -d '{"note":"chicken rice"}'
# 200 {..., "note":"chicken rice", "updated_at":"..."}   (other fields unchanged)
```

When PATCH changes `currency`, the existing amount is re-read using the new
currency's number of decimals. There is **no exchange-rate conversion**; the
number stays the same:

- `120.50` THB with `{"currency":"KWD"}` becomes `120.500` KWD (`amount_minor` 120500).
- `150.00` THB with `{"currency":"JPY"}` becomes `150` JPY (the extra zeros carry no value).
- `18.90` SGD with `{"currency":"JPY"}` returns **422**:
  `{"error":"validation failed","fields":{"amount":"the current amount 18.90 (SGD) cannot be expressed in JPY: ...; send a new amount together with the currency ..."}}`.
  Send `{"currency":"JPY","amount":"2800"}` instead.

Balances:

```bash
curl -s -X POST http://127.0.0.1:8080/api/balances -H 'Content-Type: application/json' \
  -d '{"name":"KBank Visa","type":"credit_card","currency":"THB","debt":"52000","limit":"50000"}'
```

```json
{"id":2,"uid":"a1b2c3d4-e5f6-4789-a012-3456789abcde","name":"KBank Visa","type":"credit_card","kind":"liability","currency":"THB","description":"",
 "balance":null,"balance_minor":null,"debt":"52000.00","debt_minor":5200000,"limit":"50000.00","limit_minor":5000000,
 "available":"-2000.00","available_minor":-200000,"over_limit":true,"created_at":"2026-10-07T00:21:55Z","updated_at":null}
```

```bash
curl -s 'http://127.0.0.1:8080/api/balances'
# {"balances":[...],"count":5,"totals":[
#   {"currency":"THB","assets":"95000.50","liabilities":"352000.00","net":"-256999.50",
#    "credit_limit":"550000.00","available_credit":"198000.00"}, ...]}

curl -s -X PATCH http://127.0.0.1:8080/api/balances/1 -H 'Content-Type: application/json' \
  -d '{"type":"credit_card","debt":"0","limit":"20000"}'      # asset -> liability: balance dropped
```

Per-currency totals: `assets` = sum of balances, `liabilities` = sum of debts,
`net` = assets − liabilities, `credit_limit` = sum of limits,
`available_credit` = credit_limit − liabilities. There is no currency conversion.

List with a time window (URL-encode the `+` as `%2B`; an unencoded `+` is also
accepted):

```bash
curl -s 'http://127.0.0.1:8080/api/transactions?from=2026-10-01T00:00:00%2B08:00&to=2026-10-31T23:59:59%2B08:00'
```

## Layout

```
cmd/server/main.go           entry point: flags, open DB, HTTP server, graceful shutdown
internal/money/              exact amounts: decimal string <-> integer minor units
  amount.go                  ParseAmount (> 0), ParseNonNegative (>= 0), ParseSigned, FormatAmount
  currency.go                ISO 4217 codes -> minor-unit digits
  decimal.go                 DecimalInput (JSON string or number, kept as text)
internal/validate/           ValidationError (422) and RequestError (400)
internal/transaction/            transaction domain model + validation (no I/O)
  transaction.go             Transaction, CreateInput, Validate, ParseTimestamp
  patch.go                   PATCH merge (ApplyPatch)
internal/balance/            balance domain model + per-type validation (no I/O)
  balance.go                 Type, Balance, Input, Validate, ApplyPatch, ComputeTotals
internal/store/              persistence (modernc.org/sqlite)
  store.go                   transactions in table "transactions": Open, Create, List, Get, Update, Delete
  balances.go                table "balances": CreateBalance, ListBalances, GetBalance, UpdateBalance, DeleteBalance
  migrate.go                 current schema, versioned migrations (v0 -> … -> v6)
  store_test.go, balances_test.go   fresh DB, upgrades from v0/v1/v2, partial/ambiguous states
internal/api/                Gin routes and handlers
  api.go                     transactions routes + shared helpers
  balances.go                /api/balances routes
  api_test.go, edit_test.go, balances_test.go
web/embed.go                 embeds web/static into the binary
web/static/                  index.html, style.css, app.js (transactions), balances.js (balances + tabs)
```

## Web page

Two tabs: **Transactions** (`/#transactions`, the default) and **Balances**
(`/#balances`).

Balances tab:

- A form with name, type, base currency, description, and amount fields that
  change with the type: **Balance** for payment accounts and other assets,
  **Debt** + **Limit** for credit cards and other liabilities. Hidden fields
  are not sent.
- Validation errors appear next to each field, including the duplicate-name
  error (409).
- The list is grouped by type, with a count in each heading. Assets show their
  balance; liabilities show debt, limit and available (limit − debt). A red
  "over limit" badge marks debt above the limit, and negative amounts are red.
  Each row shows the balance's `uid` in small muted monospace text (for
  debugging; the edit form does not expose it).
- **Edit** loads the balance into the form (saving sends a PUT); **Cancel edit**
  or Esc leaves edit mode. **Delete** asks for confirmation.
- A per-currency totals table shows assets, liabilities, net, credit limit and
  available credit.

Transactions tab:

- A form to log a transaction, and a table listing transactions newest first, with a
  date filter and totals per currency.
- Each row shows the transaction `uid` in small muted monospace text under
  the time (read-only; not in the form).
- **Payment account** is a dropdown of payable balances (`payment_account` and
  `credit_card`, from `GET /api/balances?payable=1`). The form submits
  `balance_uid` (the UUID). The list shows the denormalized account name.
- **Edit** loads the transaction into the same form: the card is highlighted, the
  heading reads "Edit transaction #N", and the button reads "Save changes". Saving
  sends a PUT, and validation errors appear next to each field. **Cancel edit**
  or Esc leaves edit mode without saving. Edited rows show a small "edited"
  marker; hover over it to see when.
- User data is only inserted with `textContent` / input `.value` (never
  `innerHTML`), so HTML in notes or account names is shown as text.

## Open questions

- Authentication, and whether this is single-user or multi-user.
- Currency conversion and reporting currency (e.g. totals in THB or SGD), and
  where exchange rates would come from.
- Categories/tags, receipts or attachments; an edit history / audit log
  (currently only the last `updated_at` is kept, not what changed).
- Protection against lost updates if two tabs edit the same transaction at once
  (e.g. require `updated_at` to match, or `If-Match`/ETag). Today the last save wins.
- **Follow-up:** when logging a transaction, automatically adjust the linked
  balance (lower a payment account's balance, or raise a credit card's debt).
  Not done yet — `balance_uid` is recorded only.
- Balances: should past balances be kept as dated snapshots? Should totals
  across currencies be shown in one reporting currency? Should
  `other_liability` really require a `limit`?
- Naming: the API, JSON, Go package (`internal/transaction`) and UI now say
  "transactions". Still unchanged: the module name `expense-service`, the
  `EXPENSE_*` env vars and the default DB file `data/expenses.db`. Does
  "transactions" mean income/transfers will be recorded too, which would need
  a type/sign field?
- Refunds or negative amounts (currently rejected; amounts must be > 0).
