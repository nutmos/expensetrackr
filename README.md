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
pkg/             library packages (api, store, transaction, balance, category, user,
                 money, validate), pkg/web (embedded web page) and pkg/apidoc
                 (embedded OpenAPI spec + offline Swagger UI)
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

Open http://127.0.0.1:8080/ for the web page, and
http://127.0.0.1:8080/swagger/ for the API documentation (Swagger UI).
The web page header links there too. The raw spec is at
http://127.0.0.1:8080/api/openapi.yaml (YAML) and
http://127.0.0.1:8080/api/openapi.json (JSON).

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
| uid           | `uid`          | `TEXT NOT NULL UNIQUE`, server-assigned UUID v4 (lowercase), immutable; a `uid` in PUT bodies is ignored |
| amount        | `amount`       | `amount_minor INTEGER` + `amount_scale INTEGER` (see below)             |
| amount (raw)  | `amount_minor` | the integer itself, e.g. `12050`                                       |
| currency      | `currency`     | `TEXT`, ISO 4217 alphabetic code, upper-cased (`THB`, `SGD`, `USD`, …)  |
| type          | `type`         | `TEXT NOT NULL DEFAULT 'expense'`: `expense`, `income`, `transfer`, or `balance_adjustment` (server-created only; see "Transaction types") |
| adj. direction | `adjustment_direction` | `TEXT`, `increase` / `decrease` on `balance_adjustment` rows, `NULL` (JSON `null`) otherwise |
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
There is **no SQLite foreign key**: deleting a balance leaves the transaction's
`balance_uid` and the denormalized `account` name snapshot intact.

**Balances adjust automatically** (schema v11). Creating, editing or deleting
a transaction moves its balances in the same database transaction:
- an expense lowers an account's balance or raises a card's debt;
- income raises a balance;
- a transfer moves value from source to destination.

The transaction currency must match the balance currency (422 otherwise; no
FX yet). Manual PUT still sets amounts directly and overrides those
changes (no separate adjustments API); a change of the balance/debt amount
is recorded as a read-only `balance_adjustment` transaction. Every balance and
transaction has a `version`; PUT must send it (`If-Match` or
`"version"`), else 428, and get 409 when stale. Sign rules, manual
overrides and versioning are covered in [docs/balances.md](docs/balances.md).

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
- A transaction PUT without `category_uid` (or with `""`) clears it; when
  changing `type`, omit the old category (it belongs to the old type) or send
  one of the new type.

### Users (tables `users`, `user_identities`)

User profiles, added in schema v10. Each has a `uid`, a required
`display_name` and an optional `username` and `email`, both unique and
case-insensitive. `preferences` is a free-form **JSON object**, checked by
both the app and SQLite (`json_valid`, `json_type = 'object'`), up to 32 KiB.

There are also fields reserved for future login: `password_hash` (never
exposed; the API shows only `has_password`), `password_updated_at`,
`email_verified_at`, `last_login_at` and `status` (`active` | `disabled`).
SSO links (Sign in with Apple / Google) go in `user_identities`, unique per
`(provider, provider_subject)`.

**No login, sessions or endpoint protection exist yet.** Existing data is not
tied to a user. Details and follow-ups are in
[docs/user-profile.md](docs/user-profile.md).

### Transaction types

`amount` is always positive; `type` gives the direction. Each transaction
adjusts its balances automatically:

| `type` | `balance_uid` | `to_balance_uid` |
|---|---|---|
| `expense` | asset `balance −= amount`; card `debt += amount` | n/a |
| `income` | asset `balance += amount` | n/a |
| `transfer` | source: asset `balance −= amount`, liability `debt += amount` (drawing a loan / cash advance) | destination: asset `balance += amount`, liability `debt −= amount` (card payment / loan repayment) |
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
[docs/balances.md](docs/balances.md). The table below says which balance
types each transaction type accepts:

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
- The transaction's `currency` must equal the currency of its balance(s),
  including both sides of a transfer (422 otherwise; there is no conversion).
- A PUT changing `type` away from `transfer` must omit `to_balance_uid` (or
  send `""`), else 422; changing to `transfer` requires `to_balance_uid`
  (or a 422).

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
| 10      | `CREATE TABLE IF NOT EXISTS users (...)` (uid, username, email, email_verified_at, display_name, preferences JSON object, password_hash, password_updated_at, status, last_login_at, timestamps) + unique NOCASE indexes on username and email; `CREATE TABLE IF NOT EXISTS user_identities (...)` (user_uid → users.uid ON DELETE CASCADE, provider, provider_subject, email, timestamps, UNIQUE(provider, provider_subject)) + index on user_uid. No change to existing tables. One SQL transaction, idempotent. |
| 11      | `balances` rebuilt (copy rows → drop → rename, indexes recreated, AUTOINCREMENT sequence kept) to add `version INTEGER NOT NULL DEFAULT 1` and drop `CHECK (debt_minor >= 0)` (negative debt = in credit); `transactions.version` (1) and `transactions.balance_applied` (0 for existing rows: they never moved a balance) + index on `to_balance_uid`. Existing balances are **not** recomputed. One SQL transaction, idempotent. See [docs/balances.md](docs/balances.md). |
| 12      | `DROP TABLE IF EXISTS balance_adjustments` (plus its index and sequence row). Only a pre-release build of v11 created that table (manual-edit audit, removed before release); otherwise a no-op. One SQL transaction, idempotent. |
| 13      | `transactions` rebuilt (create new → copy every row with the same ids/uids/values → drop → rename → recreate all indexes incl. unique uid → restore the AUTOINCREMENT counter) so `type` may be `balance_adjustment`; adds `adjustment_direction` (`increase`/`decrease`, NULL for existing rows) and table CHECKs tying it to that type (no destination/category, `balance_applied = 0`). One SQL transaction, idempotent. See [docs/balances.md](docs/balances.md). |

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
only on validation, conflict and duplicate-name errors.
- Some errors also carry a machine-readable `"code"`: `version_required`,
  `version_conflict` (with `"current"`, the record as it is now),
  `balance_in_use`, or `balance_adjustment_readonly`.

**Versioning** applies to balances and transactions (details in
[docs/balances.md](docs/balances.md#3-optimistic-locking-versions)):
- Responses carry `"version"` and `ETag: "<version>"`.
- PUT **must** send `If-Match: "<version>"` (preferred) or
  `"version": <n>` in the body. Neither gives **428**, a stale version gives
  **409** with the current record, and both checks come before 422.
- DELETE accepts an optional `If-Match`.

| Method | Path                 | Success | Notes |
|--------|----------------------|---------|-------|
| POST   | `/api/transactions`      | 201 + transaction, `Location` + `ETag` headers | Moves the balance(s) in the same DB transaction. 422 on validation errors (incl. currency ≠ balance currency, `type` `balance_adjustment`), 400 on malformed JSON or unknown fields |
| GET    | `/api/transactions`      | 200 `{"transactions":[...],"count":n}` | Newest first by spend time. Optional `from`, `to` (RFC 3339 with offset, both inclusive), `limit` (1–5000, default 500), `type` (`expense`/`income`/`transfer`/`balance_adjustment`; 400 if invalid), `category_uid` (UUID; 400 if malformed) |
| GET    | `/api/transactions/:uid`  | 200 + transaction | `:uid` is the transaction UUID (case-insensitive). **400** if it is not UUID-shaped (including numeric ids such as `/api/transactions/1`), **404** if no such uid. Same for PUT/DELETE |
| PUT    | `/api/transactions/:uid`  | 200 + updated transaction | Full replace with the same body and rules as POST. An omitted `note` clears it. Requires `If-Match` or `"version"` (428 / 409). Reverses the old balance effect and applies the new one. 409 `balance_adjustment_readonly` on a balance adjustment (also DELETE). 404 if missing, 422 for invalid values, 400 for malformed JSON or unknown fields |
| DELETE | `/api/transactions/:uid`  | 204 | Reverses the balance effect. Optional `If-Match` (409 if stale). 404 if missing |
| POST   | `/api/balances`      | 201 + balance, `Location` header | 422 on validation errors (incl. per-type amount rules), 409 if the name is already used (case-insensitive), 400 on malformed JSON or unknown fields |
| GET    | `/api/balances`      | 200 `{"balances":[...],"count":n,"totals":[...]}` | Grouped by type, then by name. Optional `type=` filter (400 if invalid). Optional `payable=1` / `payable=true` returns only `payment_account` and `credit_card` (cannot combine with `type=`). `totals` covers the returned rows, per currency |
| GET    | `/api/balances/:uid`  | 200 + balance | `:uid` is the balance UUID. 400 if not UUID-shaped (numeric ids are no longer accepted), 404 if missing. Same for PUT/DELETE |
| PUT    | `/api/balances/:uid`  | 200 + updated balance | Full replace (manual override of the amounts; a balance/debt change records a `balance_adjustment` transaction). `type` is required and must equal the stored type (422 on `type` otherwise). `uid` in the body is ignored. Requires `If-Match` or `"version"` (428 / 409). 409 `balance_in_use` for a currency change while transactions reference it. Returns 404, 409, 422 or 400 as for POST |
| DELETE | `/api/balances/:uid`  | 204 | Optional `If-Match` (409 if stale). 404 if missing |
| POST   | `/api/categories`      | 201 + category, `Location: /api/categories/<uid>` | 422 on validation errors, 409 duplicate name within the type, 400 malformed JSON / unknown fields |
| GET    | `/api/categories`      | 200 `{"categories":[...],"count":n}` | Expense first, then income, by name. Optional `type=expense\|income` (400 if invalid) |
| GET    | `/api/categories/:uid` | 200 + category | 400 if not UUID-shaped, 404 if missing |
| PUT    | `/api/categories/:uid` | 200 + updated category | Full replace (omitted description clears it). 409 duplicate name, or type change while referenced by transactions |
| DELETE | `/api/categories/:uid` | 204 | 409 if any transaction references it; 404 if missing |
| POST   | `/api/users`           | 201 + user, `Location: /api/users/<uid>` | `display_name` required; `username`, `email`, `preferences` (JSON object) optional. 422 invalid, 409 duplicate username/email, 400 unknown fields (incl. `password`, `password_hash`) |
| GET    | `/api/users`           | 200 `{"users":[...],"count":n}` | Oldest first |
| GET    | `/api/users/:uid`      | 200 + user | Never includes `password_hash`; `has_password` instead |
| PUT    | `/api/users/:uid`      | 200 + updated user | Full replace of profile fields (omitted username/email removed, preferences → `{}`). Read-only fields in the body are ignored |
| PATCH  | `/api/users/:uid`      | 200 + updated user | Partial; `preferences` **replaces** the whole object (`null` → `{}`); `username`/`email` `null` removes |
| DELETE | `/api/users/:uid`      | 204 | Also deletes linked identities |
| GET    | `/api/users/:uid/identities` | 200 `{"identities":[...],"count":n}` | Read-only SSO links (empty until SSO exists) |
| GET    | `/api/healthz`       | 200 `{"status":"ok"}` | |
| GET    | `/api/openapi.yaml`  | 200 the OpenAPI 3.0 document | Hand-written spec, embedded in the binary |
| GET    | `/api/openapi.json`  | 200 the same document as JSON | |
| GET    | `/swagger/`, `/swagger` | Swagger UI (HTML) | `/swagger` redirects to `/swagger/`. Assets are vendored and embedded (offline; no CDN) |
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
  -d '{"name":"Wallet","type":"payment_account","currency":"THB","balance":"10000"}' \
  | python3 -c 'import sys,json; print(json.load(sys.stdin)["uid"])')

curl -s -X POST http://127.0.0.1:8080/api/transactions \
  -H 'Content-Type: application/json' \
  -d "{\"amount\":\"120.50\",\"currency\":\"THB\",\"balance_uid\":\"$BAL_UID\",\"spent_at\":\"2026-10-06T21:06:00+08:00\",\"note\":\"Lunch\"}"
```

```json
{"uid":"5f0c2e9a-7b1d-4c3e-9a8f-2d6b1e4c7a90","amount":"120.50","amount_minor":12050,"currency":"THB",
 "balance_uid":"a1b2c3d4-e5f6-4789-a012-3456789abcde","account":"Wallet",
 "spent_at":"2026-10-06T21:06:00+08:00","note":"Lunch","created_at":"2026-10-06T13:20:11Z"}
```

Transfer (pay a credit card from a bank account):

```bash
curl -s -X POST http://127.0.0.1:8080/api/transactions -H 'Content-Type: application/json' \
  -d "{\"type\":\"transfer\",\"amount\":\"5000\",\"currency\":\"THB\",\"balance_uid\":\"$BAL_UID\",\"to_balance_uid\":\"$CARD_UID\",\"spent_at\":\"2026-10-07T22:00:00+08:00\"}"
```

```json
{"uid":"…","type":"transfer","amount":"5000.00","amount_minor":500000,"currency":"THB",
 "balance_uid":"…","account":"Wallet","to_balance_uid":"…","to_account":"Credit Card",
 "spent_at":"2026-10-07T22:00:00+08:00","note":"","created_at":"…","updated_at":null}
```

Expense and income rows return `"to_balance_uid":null,"to_account":null`.
`GET /api/transactions?type=income` lists only income;
`?type=balance_adjustment` lists only balance adjustments.

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

Edit: full replace with PUT (there is no PATCH for transactions, balances or
categories; a PATCH there is a 404 like any unknown method). Send the **whole
record**: every editable field, plus the version (`If-Match` or `"version"`).
PUT keeps `uid` and `created_at` and sets `updated_at`:

```bash
curl -s -X PUT http://127.0.0.1:8080/api/transactions/$TX_UID -H 'Content-Type: application/json' \
  -H 'If-Match: "1"' \
  -d "{\"amount\":\"150\",\"currency\":\"THB\",\"balance_uid\":\"$BAL_UID\",\"spent_at\":\"2026-10-05T09:15:00+07:00\",\"note\":\"pad thai + drink\"}"
# 200 {"uid":"…","amount":"150.00","amount_minor":15000,"currency":"THB",...,"updated_at":"2026-10-06T13:18:11Z"}
```

To change one field, GET the record, change that field and PUT all editable
fields back. **Optional fields are cleared by omitting them (or sending
`""`)**: `note`, `category_uid`, `to_balance_uid` on a transaction,
`description` on a category or balance. A changed `currency` needs an `amount` that fits the
new currency's decimals (there is no exchange-rate conversion).

Balances:

```bash
curl -s -X POST http://127.0.0.1:8080/api/balances -H 'Content-Type: application/json' \
  -d '{"name":"Credit Card","type":"credit_card","currency":"THB","debt":"52000","limit":"50000"}'
```

```json
{"uid":"a1b2c3d4-e5f6-4789-a012-3456789abcde","name":"Credit Card","type":"credit_card","kind":"liability","currency":"THB","description":"",
 "balance":null,"balance_minor":null,"debt":"52000.00","debt_minor":5200000,"limit":"50000.00","limit_minor":5000000,
 "available":"-2000.00","available_minor":-200000,"over_limit":true,"created_at":"2026-10-07T00:21:55Z","updated_at":null}
```

```bash
curl -s 'http://127.0.0.1:8080/api/balances'
# {"balances":[...],"count":5,"totals":[
#   {"currency":"THB","assets":"95000.50","liabilities":"352000.00","net":"-256999.50",
#    "credit_limit":"550000.00","available_credit":"198000.00"}, ...]}

curl -s -X PUT http://127.0.0.1:8080/api/balances/$BAL_UID -H 'Content-Type: application/json' \
  -H 'If-Match: "1"' -d '{"name":"Cash","type":"payment_account","currency":"THB","balance":"1200.50"}'
# full record; ETag of the last GET. The balance change records a balance_adjustment.

curl -s -X PUT http://127.0.0.1:8080/api/balances/$BAL_UID -H 'Content-Type: application/json' \
  -d '{"version":2,"name":"Cash","type":"credit_card","currency":"THB","debt":"0","limit":"0"}'
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
  input.go                         Transaction.Input (stored record -> CreateInput)
pkg/category/                      category domain model (Type expense|income, Input, Validate)
pkg/user/                          user profile model: Input, Validate (preferences JSON object), ApplyPatch, Identity
pkg/balance/                       balance domain model + per-type validation (no I/O)
  balance.go                       Type, Balance, Input, Validate, ComputeTotals
pkg/store/                         persistence (modernc.org/sqlite)
  store.go                         transactions in table "transactions": Open, Create, List, Get, Update(With), Delete(With); WriteOptions, ErrVersionConflict
  effects.go                       applyBalanceEffects: move balances for a transaction write (same DB transaction)
  balances.go                      table "balances": CreateBalance, ListBalances, GetBalance, UpdateBalance(With), DeleteBalance(With)
  adjustments.go                   balance_adjustment transactions recorded by manual balance edits
  categories.go                    table "categories": CreateCategory, ListCategories, GetCategoryByUID, UpdateCategory, DeleteCategory
  users.go                         tables "users", "user_identities": CreateUser, ListUsers, GetUserByUID, UpdateUser, DeleteUser, SetPasswordHash, CreateIdentity, ListIdentities, FindUserByIdentity
  migrate.go                       current schema, versioned migrations (v0 -> … -> v13)
  store_test.go, balances_test.go  fresh DB, upgrades from v0/v1/v2, partial/ambiguous states
pkg/api/                           Gin routes and handlers
  api.go                           router setup + shared helpers
  transactions.go                  /api/transactions routes
  balances.go                      /api/balances routes
  version.go                       optimistic locking: If-Match / "version", ETag, 428 / 409
  categories.go                    /api/categories routes
  users.go                         /api/users routes (+ read-only /identities)
  pages.go                         web page routes (list / new / edit paths → index.html)
  *_test.go                        handler tests (throwaway DB per test)
pkg/web/embed.go                   embeds web/static into the binary
pkg/web/static/                    index.html (all views), style.css, router.js (history router + shared helpers),
                                   transactions.js, balances.js, categories.js (list + form page each)
pkg/apidoc/                        the API description, embedded into the binary
  openapi.yaml                     hand-written OpenAPI 3.0 spec (the source of truth for the docs)
  apidoc.go                        serves GET /api/openapi.yaml, /api/openapi.json and /swagger/
  swagger-ui/                      vendored swagger-ui-dist 5.33.1 (Apache-2.0; see VENDOR.md)
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
| `/swagger/`                  | API documentation (Swagger UI; opens in a new tab from the header) |

Old hash links (`/#balances`, `/#categories`, `/#transactions`) are rewritten
to the matching path.

**API docs.** The header's **API docs** link opens `/swagger/` (Swagger UI)
in a new tab. The spec is hand-written (`pkg/apidoc/openapi.yaml`) and embedded
in the binary, and the Swagger UI assets are vendored (`swagger-ui-dist`
5.33.1, Apache-2.0), so the docs work without network access. A test
(`pkg/api/openapi_test.go`) fails when a `/api` route is missing from the
spec, or a spec operation has no route.

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
  separately; transfers and balance adjustments are excluded from both.
- **Balance Adjustment** rows (recorded by manual balance edits) have their
  own style and label, a signed amount (+/−), and "Automatic" instead of
  Edit/Delete; their edit URL shows a read-only message.
- Columns: time (shown in the device's time zone, with the stored value on
  hover, and an "edited" marker), Type, Amount,
  Currency, Account ("from → to" for transfers), Category, Note.
- The category name is looked up client-side from `/api/categories` by
  `category_uid`.
- The form has a **Type** selector (Expense / Income / Transfer; balance
  adjustments cannot be entered). The source
  dropdown lists only the balance types allowed for that type. Its label
  changes to "Payment account", "Received into" or "From (source)". A **To
  (destination)** dropdown appears only for transfers.
- **Category** lists only categories of the selected type and is hidden for
  transfers.
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

## Open questions

- Authentication: the schema is ready (see docs/user-profile.md), but login,
  sessions, SSO and per-user scoping of transactions, balances and categories
  are follow-ups.
- Currency conversion and reporting currency (e.g. totals in THB or SGD), and
  where exchange rates would come from.
- Categories/tags, receipts or attachments; an edit history / audit log
  (currently only the last `updated_at` is kept, not what changed).
- Optimistic locking covers balances and transactions; categories and users
  are not versioned yet.
- FX for cross-currency transactions/transfers; an optional "recompute
  balances from transactions" / reconciliation tool (see docs/balances.md).
- Balances: should past balances be kept as dated snapshots? Should totals
  across currencies be shown in one reporting currency? Should
  `other_liability` really require a `limit`?
- Naming: the API, JSON, Go package (`pkg/transaction`) and UI now say
  "transactions". Still unchanged: the module name `expense-service`, the
  `EXPENSE_*` env vars and the default DB file `data/expenses.db`. Does
  "transactions" mean income/transfers will be recorded too, which would need
  a type/sign field?
- Refunds or negative amounts (currently rejected; amounts must be > 0).
