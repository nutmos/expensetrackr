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
- GNU make (optional; the targets are thin wrappers around `go` commands)

## Repository layout

Modelled on [prometheus-operator](https://github.com/prometheus-operator/prometheus-operator):
`go.mod` at the repo root, binaries under `cmd/`, packages under `pkg/`, a
`Makefile` for common tasks, and documentation in its own folder. Everything
runs from the repo root; no `cd` needed.

```
go.mod, go.sum   module github.com/nutmos/expensetrackr
Makefile         build / run / test / vet / fmt / clean
cmd/server/      the expense-server binary (main package)
pkg/             library packages (api, store, transaction, balance, category,
                 money, validate) and pkg/web (embedded web page)
docs/            project documentation (non-code)
README.md
bin/, data/, server.log   local runtime files (git-ignored)
```

Import packages as `github.com/nutmos/expensetrackr/pkg/<name>`. The web page
lives in `pkg/web` (an embed package like any other), not a root `web/` folder,
so all Go code other than `cmd/` lives under `pkg/`.

## Run

```bash
cd /workspace/expense-service    # repo root
make run                         # build bin/expense-server, serve http://127.0.0.1:8080 with data/expenses.db

# the same without make
CGO_ENABLED=0 go build -o bin/expense-server ./cmd/server
./bin/expense-server -addr 127.0.0.1:8080 -db data/expenses.db

# or straight from source
go run ./cmd/server              # defaults: 127.0.0.1:8080, data/expenses.db
```

`make run` takes overrides: `make run ADDR=127.0.0.1:9090 DB=/tmp/test.db`.
The default `-db data/expenses.db` is relative to the current directory, so
run from the repo root.

| Flag    | Env var        | Default            | Notes                                       |
|---------|----------------|--------------------|---------------------------------------------|
| `-addr` | `EXPENSE_ADDR` | `127.0.0.1:8080`   | Keep on 127.0.0.1: there is no auth (`:8080` would expose it on all interfaces) |
| `-db`   | `EXPENSE_DB`   | `data/expenses.db` | Created automatically (directory included)  |

Set `GIN_MODE=release` to silence Gin's debug output.

Run in the background (from the repo root):

```bash
make build
nohup ./bin/expense-server -db data/expenses.db > server.log 2>&1 &
```

Open http://127.0.0.1:8080/ for the web page.

## Test

```bash
make check        # fmt-check + vet + test
# or individually
make fmt          # gofmt -s -w cmd pkg
make vet          # go vet ./...
make test         # go test ./...
```

| Target      | Does                                                        |
|-------------|-------------------------------------------------------------|
| `build`     | `go build -o bin/expense-server ./cmd/server`               |
| `run`       | `build`, then `./bin/expense-server -addr $(ADDR) -db $(DB)` (defaults `127.0.0.1:8080`, `data/expenses.db`) |
| `test`      | `go test ./...`                                             |
| `vet`       | `go vet ./...`                                              |
| `fmt`       | `gofmt -s -w cmd pkg`                                       |
| `fmt-check` | fails if any file needs gofmt                               |
| `check`     | `fmt-check` + `vet` + `test`                                |
| `clean`     | `rm -rf bin` (never touches `data/`)                        |
| `help`      | lists targets (default)                                     |

The Makefile exports `GOTOOLCHAIN=local` and `CGO_ENABLED=0` unless already set.

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
| id            | (not exposed)  | `INTEGER PRIMARY KEY AUTOINCREMENT`, internal only; the API uses `uid`  |
| uid           | `uid`          | `TEXT NOT NULL UNIQUE`, server-assigned UUID v4 (lowercase), immutable; a `uid` in PUT/PATCH bodies is ignored |
| amount        | `amount`       | `amount_minor INTEGER` + `amount_scale INTEGER` (see below)             |
| amount (raw)  | `amount_minor` | the integer itself, e.g. `12050`                                       |
| currency      | `currency`     | `TEXT`, ISO 4217 alphabetic code, upper-cased (`THB`, `SGD`, `USD`, …)  |
| type          | `type`         | `TEXT NOT NULL DEFAULT 'expense'`: `expense`, `income` or `transfer` (see "Transaction types") |
| payment acct  | `balance_uid`  | `TEXT` UUID of the balance paid from (expense), received into (income) or moved from (transfer); no FK |
| destination   | `to_balance_uid` | `TEXT`, transfers only (else `null`); UUID of the destination balance; no FK |
| dest. name    | `to_account`   | denormalized destination name snapshot (transfers only, else `null`)    |
| category      | `category_uid` | `TEXT`, optional (`null` = none); UUID of a category of the same type; never set for transfers; no FK. **The only link to the category**: no name is stored on the transaction |
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

The **type is fixed at creation**: a `credit_card` can never become a
`payment_account`, etc. PUT still requires `type`, and both PUT and PATCH accept
it only when it equals the stored type (case-insensitive), so sending back a
GET response works. A different type is rejected with **422** and
`"fields":{"type":"balance type cannot be changed after creation"}`. Other
fields are still checked against the stored type in the same response, and
nothing is changed. To change the type, create a new balance and delete the
old one.

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

### Categories (table `categories`)

A **category** classifies expenses and income. Fields: server-assigned
immutable `uid` (UUID v4), `name` (required, ≤ 100 chars), `type`
(`expense` or `income`), optional `description` (≤ 1000), `created_at`,
`updated_at`. Names are unique **case-insensitively within a type**
(`Food` as expense and `food` as income may coexist); a duplicate is 409.

Rules linking transactions and categories:

- `category_uid` is optional on expense and income transactions. An expense
  must use an `expense` category and income an `income` category (422 on
  `category_uid` otherwise, also for unknown or malformed uids). A transfer
  must not have a category (422).
- The transaction stores **only `category_uid`** (no name copy, unlike
  `account`). Clients look the name up from `GET /api/categories` (the web
  page does this), so renaming a category is reflected everywhere at once.
- **Deleting** a category that any transaction references is rejected with
  **409**; clear or change `category_uid` on those transactions first.
- **Changing a category's type** is rejected with **409** while any
  transaction references it (those transactions would no longer match); an
  unreferenced category may change type freely. Renaming is always allowed.
- PATCH on a transaction that changes `type` without sending `category_uid`
  drops the category (it belonged to the old type); `"category_uid": null`
  clears it; a PUT without `category_uid` clears it.

### Transaction types

`amount` is always positive; `type` gives the direction. Balance amounts are
**not** adjusted automatically yet (see Open questions).

| `type`     | `balance_uid` must be                         | `to_balance_uid` |
|------------|-----------------------------------------------|------------------|
| `expense`  | `payment_account` or `credit_card` (spent from) | not allowed (422) |
| `income`   | `payment_account` or `other_asset` (received into) | not allowed (422) |
| `transfer` | any balance type (source)                     | **required**; any existing balance type, different from `balance_uid` |

- Omitting `type` on POST or PUT means `expense`, so older clients keep working.
  A PUT is a full replace, so a PUT of a transfer must send `"type":"transfer"`.
- Why: income lands in a spendable account or an asset (salary, dividends);
  a credit card or loan receiving money is modelled as a transfer (card
  payment, loan repayment). Transfers may also move money into an
  `other_asset` (savings) or draw from a credit card / loan.
- No currency check across the two balances of a transfer (there is no
  conversion; the amount is in the transaction's `currency`).
- PATCH changing `type` away from `transfer` drops `to_balance_uid` /
  `to_account` automatically; changing to `transfer` requires
  `to_balance_uid` in the same PATCH (or a 422). `"to_balance_uid": null`
  clears it.

### Amounts: integer minor units (no floats)

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

### Spend date-time: RFC 3339 with offset

`spent_at` must be a full RFC 3339 / ISO 8601 date-time **with** a UTC offset,
e.g. `2026-10-06T21:06:00+08:00` or `2026-10-06T13:06:00Z`. Values without an
offset, date-only values and other formats are rejected. The value is stored at
second precision with the **original offset kept** (so you can still see that a
Bangkok transaction was entered at +07:00). The derived `spent_at_unix` column holds
the absolute instant and is used for ordering and filtering, so rows entered with
different offsets compare correctly.

The web form has only a date-time field: it always uses the **device's current
time zone**. On create and edit the entered wall-clock time is sent with the
device's UTC offset for that date (DST-aware); there is no manual offset field.
When you edit a record saved with a different offset (e.g. `09:15+07:00` on a
`+08:00` device), the form shows it **converted to device local time**
(10:15) and saving re-stamps it with the device offset
(`10:15+08:00`, the same instant). The list also shows times in device local
time; hover a time to see the stored value. The API is unchanged and still
accepts any RFC 3339 offset.

### Schema migrations

The schema version is tracked in SQLite's `PRAGMA user_version`. The code is in
`pkg/store/migrate.go`.

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
| 7       | `ALTER TABLE transactions ADD COLUMN type TEXT NOT NULL DEFAULT 'expense' CHECK (type IN ('expense','income','transfer'))` (existing rows become `expense`), `ADD COLUMN to_balance_uid TEXT` and `ADD COLUMN to_account TEXT` (each only if missing), then `CREATE INDEX IF NOT EXISTS idx_transactions_type ON transactions (type, spent_at_unix DESC)`. One SQL transaction, idempotent. |
| 8       | `CREATE TABLE IF NOT EXISTS categories (...)` + `CREATE UNIQUE INDEX IF NOT EXISTS idx_categories_type_name ON categories (type, name COLLATE NOCASE)`, then `ALTER TABLE transactions ADD COLUMN category_uid TEXT` (only if missing; existing rows get no category) and `CREATE INDEX IF NOT EXISTS idx_transactions_category_uid ON transactions (category_uid)`. One SQL transaction, idempotent. |
| 9       | Drop `transactions.category` if present (`ALTER TABLE transactions DROP COLUMN category`, SQLite ≥ 3.35). Only databases created by a pre-release build of v8, which stored a category name copy, have it; otherwise a no-op. One SQL transaction, idempotent. |

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

Resources are identified **only by `uid`** (UUID v4) in URLs and JSON;
the internal integer row id is never returned or accepted. `Location`
headers on create are `/api/transactions/<uid>` and `/api/balances/<uid>`.

All responses are JSON. Every error looks like
`{"error": "message", "fields": {"field": "problem", ...}}`. `fields` appears
only on validation and duplicate-name errors.

| Method | Path                 | Success | Notes |
|--------|----------------------|---------|-------|
| POST   | `/api/transactions`      | 201 + transaction, `Location` header | 422 on validation errors, 400 on malformed JSON or unknown fields |
| GET    | `/api/transactions`      | 200 `{"transactions":[...],"count":n}` | Newest first by spend time. Optional `from`, `to` (RFC 3339 with offset, both inclusive), `limit` (1–5000, default 500), `type` (`expense`/`income`/`transfer`; 400 if invalid), `category_uid` (UUID; 400 if malformed) |
| GET    | `/api/transactions/:uid`  | 200 + transaction | `:uid` is the transaction UUID (case-insensitive). **400** if it is not UUID-shaped (including numeric ids such as `/api/transactions/1`), **404** if no such uid. Same for PUT/PATCH/DELETE |
| PUT    | `/api/transactions/:uid`  | 200 + updated transaction | Full replace with the same body and rules as POST. An omitted `note` clears it. 404 if missing, 422 for invalid values, 400 for malformed JSON or unknown fields |
| PATCH  | `/api/transactions/:uid`  | 200 + updated transaction | Partial update: only the fields you send change, then the merged result is validated like a create. `"note": null` clears the note; `null` for any other field returns 422. Returns 400 for `{}`, unknown fields or wrong JSON types, and 404 if missing |
| DELETE | `/api/transactions/:uid`  | 204 | 404 if missing |
| POST   | `/api/balances`      | 201 + balance, `Location` header | 422 on validation errors (incl. per-type amount rules), 409 if the name is already used (case-insensitive), 400 on malformed JSON or unknown fields |
| GET    | `/api/balances`      | 200 `{"balances":[...],"count":n,"totals":[...]}` | Grouped by type, then by name. Optional `type=` filter (400 if invalid). Optional `payable=1` / `payable=true` returns only `payment_account` and `credit_card` (cannot combine with `type=`). `totals` covers the returned rows, per currency |
| GET    | `/api/balances/:uid`  | 200 + balance | `:uid` is the balance UUID. 400 if not UUID-shaped (numeric ids are no longer accepted), 404 if missing. Same for PUT/PATCH/DELETE |
| PUT    | `/api/balances/:uid`  | 200 + updated balance | Full replace. `type` is required and must equal the stored type (422 on `type` otherwise). `uid` in the body is ignored. Returns 404, 409, 422 or 400 as for POST |
| PATCH  | `/api/balances/:uid`  | 200 + updated balance | Partial update. `type` may be sent only with the stored value (422 on `type` otherwise). `"description": null` clears it; `"balance"`/`"debt"`/`"limit": null` removes that amount. A currency change re-reads the amounts using the new currency's decimals. `uid` in the body is ignored |
| DELETE | `/api/balances/:uid`  | 204 | 404 if missing |
| POST   | `/api/categories`      | 201 + category, `Location: /api/categories/<uid>` | 422 on validation errors, 409 duplicate name within the type, 400 malformed JSON / unknown fields |
| GET    | `/api/categories`      | 200 `{"categories":[...],"count":n}` | Expense first, then income, by name. Optional `type=expense\|income` (400 if invalid) |
| GET    | `/api/categories/:uid` | 200 + category | 400 if not UUID-shaped, 404 if missing |
| PUT    | `/api/categories/:uid` | 200 + updated category | Full replace (omitted description clears it). 409 duplicate name, or type change while referenced by transactions |
| PATCH  | `/api/categories/:uid` | 200 + updated category | Partial update of `name`, `type`, `description` (`null` clears description). Same 409 rules |
| DELETE | `/api/categories/:uid` | 204 | 409 if any transaction references it; 404 if missing |
| GET    | `/api/healthz`       | 200 `{"status":"ok"}` | |
| GET    | `/`, `/transactions`, `/balances`, `/categories` | HTML page (list) | Same `index.html` for every page path; the client-side router picks the view. Static assets under `/static/` |
| GET    | `/<res>/new`, `/<res>/:uid/edit` | HTML page (add / edit form) | `<res>` = `transactions`, `balances` or `categories`. `:uid` must be UUID-shaped (any case), else 404; an unknown uid is reported by the page itself. Other paths → 404 JSON |

### Examples

Categories:

```bash
CAT_UID=$(curl -s -X POST http://127.0.0.1:8080/api/categories -H 'Content-Type: application/json' \
  -d '{"name":"Food","type":"expense","description":"Groceries and restaurants"}' \
  | python3 -c 'import sys,json; print(json.load(sys.stdin)["uid"])')
# 201 {"uid":"…","name":"Food","type":"expense","description":"Groceries and restaurants","created_at":"…","updated_at":null}

curl -s -X POST http://127.0.0.1:8080/api/transactions -H 'Content-Type: application/json' \
  -d "{\"amount\":\"120.50\",\"currency\":\"THB\",\"balance_uid\":\"$BAL_UID\",\"category_uid\":\"$CAT_UID\",\"spent_at\":\"2026-10-06T21:06:00+08:00\"}"
# 201 {..., "category_uid":"…", ...}   (no category name on the transaction)

curl -s "http://127.0.0.1:8080/api/transactions?category_uid=$CAT_UID"
curl -s -X DELETE "http://127.0.0.1:8080/api/categories/$CAT_UID"
# 409 {"error":"category is used by transactions (1 transaction(s)); it cannot be deleted; reassign or clear category_uid on those transactions first"}
```

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
{"uid":"5f0c2e9a-7b1d-4c3e-9a8f-2d6b1e4c7a90","amount":"120.50","amount_minor":12050,"currency":"THB",
 "balance_uid":"a1b2c3d4-e5f6-4789-a012-3456789abcde","account":"KBank debit",
 "spent_at":"2026-10-06T21:06:00+08:00","note":"Lunch","created_at":"2026-10-06T13:20:11Z"}
```

Transfer (pay a credit card from a bank account):

```bash
curl -s -X POST http://127.0.0.1:8080/api/transactions -H 'Content-Type: application/json' \
  -d "{\"type\":\"transfer\",\"amount\":\"5000\",\"currency\":\"THB\",\"balance_uid\":\"$BAL_UID\",\"to_balance_uid\":\"$CARD_UID\",\"spent_at\":\"2026-10-07T22:00:00+08:00\"}"
```

```json
{"uid":"…","type":"transfer","amount":"5000.00","amount_minor":500000,"currency":"THB",
 "balance_uid":"…","account":"KBank debit","to_balance_uid":"…","to_account":"KBank Visa",
 "spent_at":"2026-10-07T22:00:00+08:00","note":"","created_at":"…","updated_at":null}
```

Expense and income rows return `"to_balance_uid":null,"to_account":null`.
`GET /api/transactions?type=income` lists only income.

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

Edit: full replace (PUT) and partial update (PATCH). Both keep `uid` and
`created_at` and set `updated_at`:

```bash
curl -s -X PUT http://127.0.0.1:8080/api/transactions/$TX_UID -H 'Content-Type: application/json' \
  -d "{\"amount\":\"150\",\"currency\":\"THB\",\"balance_uid\":\"$BAL_UID\",\"spent_at\":\"2026-10-05T09:15:00+07:00\",\"note\":\"pad thai + drink\"}"
# 200 {"uid":"…","amount":"150.00","amount_minor":15000,"currency":"THB",...,"updated_at":"2026-10-06T13:18:11Z"}

curl -s -X PATCH http://127.0.0.1:8080/api/transactions/$TX_UID -H 'Content-Type: application/json' \
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
{"uid":"a1b2c3d4-e5f6-4789-a012-3456789abcde","name":"KBank Visa","type":"credit_card","kind":"liability","currency":"THB","description":"",
 "balance":null,"balance_minor":null,"debt":"52000.00","debt_minor":5200000,"limit":"50000.00","limit_minor":5000000,
 "available":"-2000.00","available_minor":-200000,"over_limit":true,"created_at":"2026-10-07T00:21:55Z","updated_at":null}
```

```bash
curl -s 'http://127.0.0.1:8080/api/balances'
# {"balances":[...],"count":5,"totals":[
#   {"currency":"THB","assets":"95000.50","liabilities":"352000.00","net":"-256999.50",
#    "credit_limit":"550000.00","available_credit":"198000.00"}, ...]}

curl -s -X PATCH http://127.0.0.1:8080/api/balances/$BAL_UID -H 'Content-Type: application/json' \
  -d '{"balance":"1200.50"}'                                    # partial update

curl -s -X PATCH http://127.0.0.1:8080/api/balances/$BAL_UID -H 'Content-Type: application/json' \
  -d '{"type":"credit_card"}'
# 422 {"error":"validation failed","fields":{"type":"balance type cannot be changed after creation"}}
```

Per-currency totals: `assets` = sum of balances, `liabilities` = sum of debts,
`net` = assets − liabilities, `credit_limit` = sum of limits,
`available_credit` = credit_limit − liabilities. There is no currency conversion.

List with a time window (URL-encode the `+` as `%2B`; an unencoded `+` is also
accepted):

```bash
curl -s 'http://127.0.0.1:8080/api/transactions?from=2026-10-01T00:00:00%2B08:00&to=2026-10-31T23:59:59%2B08:00'
```

## Code layout

Paths are relative to the repo root (the module root).

```
cmd/server/main.go                 entry point: flags, open DB, HTTP server, graceful shutdown
pkg/money/                         exact amounts: decimal string <-> integer minor units
  amount.go                        ParseAmount (> 0), ParseNonNegative (>= 0), ParseSigned, FormatAmount
  currency.go                      ISO 4217 codes -> minor-unit digits
  decimal.go                       DecimalInput (JSON string or number, kept as text)
pkg/validate/                      ValidationError (422) and RequestError (400)
pkg/transaction/                   transaction domain model + validation (no I/O)
  transaction.go                   Transaction, CreateInput, Validate, ParseTimestamp
  patch.go                         PATCH merge (ApplyPatch)
pkg/category/                      category domain model (Type expense|income, Input, Validate, ApplyPatch)
pkg/balance/                       balance domain model + per-type validation (no I/O)
  balance.go                       Type, Balance, Input, Validate, ApplyPatch, ComputeTotals
pkg/store/                         persistence (modernc.org/sqlite)
  store.go                         transactions in table "transactions": Open, Create, List, Get, Update, Delete
  balances.go                      table "balances": CreateBalance, ListBalances, GetBalance, UpdateBalance, DeleteBalance
  categories.go                    table "categories": CreateCategory, ListCategories, GetCategoryByUID, UpdateCategory, DeleteCategory
  migrate.go                       current schema, versioned migrations (v0 -> … -> v9)
  store_test.go, balances_test.go  fresh DB, upgrades from v0/v1/v2, partial/ambiguous states
pkg/api/                           Gin routes and handlers
  api.go                           router setup + shared helpers
  transactions.go                  /api/transactions routes
  balances.go                      /api/balances routes
  categories.go                    /api/categories routes
  pages.go                         web page routes (list / new / edit paths → index.html)
  *_test.go                        handler tests (throwaway DB per test)
pkg/web/embed.go                   embeds web/static into the binary
pkg/web/static/                    index.html (all views), style.css, router.js (history router + shared helpers),
                                   transactions.js, balances.js, categories.js (list + form page each)
```

## Web page

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

Old hash links (`/#balances`, `/#categories`, `/#transactions`) are rewritten
to the matching path.

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
  zone) and per-currency totals. Expenses and income are summed separately, and
  transfers are excluded from both.
- Columns: time (shown in the device's time zone, with the stored value on
  hover, an "edited" marker, and the uid in small muted text), Type, Amount,
  Currency, Account ("from → to" for transfers), Category, Note.
- The category name is looked up client-side from `/api/categories` by
  `category_uid`.
- The form has a **Type** selector (Expense / Income / Transfer). The source
  dropdown lists only the balance types allowed for that type. Its label
  changes to "Payment account", "Received into" or "From (source)". A **To
  (destination)** dropdown appears only for transfers.
- **Category** lists only categories of the selected type and is hidden for
  transfers.
- Time is entered in the device's local time zone. A preview shows the exact
  RFC 3339 value that will be saved, with the device's offset for that date.
- The add page remembers the last expense currency and account.

**Balances**

- The list is grouped by type, with counts. Assets show their balance.
  Liabilities show debt, limit and available. A red "over limit" badge marks
  debt above the limit, and negative amounts are red. There is also a
  per-currency totals table.
- The form's amount fields follow the type: **Balance** for assets,
  **Debt** + **Limit** for liabilities. Hidden fields are not sent.
- On the edit page the **Type** selector is disabled, because types can't
  change after creation.

**Categories**

- The list is grouped into expense and income categories.
- **Delete** of a category still used by transactions shows the 409 message
  on the list.

User data is only inserted with `textContent` / input `.value` (never
`innerHTML`), so HTML in notes or names is shown as text.

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
- Naming: the API, JSON, Go package (`pkg/transaction`) and UI now say
  "transactions". Still unchanged: the module name `expense-service`, the
  `EXPENSE_*` env vars and the default DB file `data/expenses.db`. Does
  "transactions" mean income/transfers will be recorded too, which would need
  a type/sign field?
- Refunds or negative amounts (currently rejected; amounts must be > 0).
