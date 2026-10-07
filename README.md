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

Expenses are stored in the SQLite table **`transactions`**, with index
`idx_transactions_spent_at_unix` on `(spent_at_unix DESC, id DESC)`. Until schema
version 2 the table was named `expenses`; see "Schema migrations". The HTTP API
and JSON still use "expenses" (`/api/expenses`, `{"expenses": [...]}`).

| Field         | JSON           | Stored as                                                              |
|---------------|----------------|------------------------------------------------------------------------|
| id            | `id`           | `INTEGER PRIMARY KEY AUTOINCREMENT`                                    |
| amount        | `amount`       | `amount_minor INTEGER` + `amount_scale INTEGER` (see below)             |
| amount (raw)  | `amount_minor` | the integer itself, e.g. `12050`                                       |
| currency      | `currency`     | `TEXT`, ISO 4217 alphabetic code, upper-cased (`THB`, `SGD`, `USD`, …)  |
| payment acct  | `account`      | `TEXT`, free text (`KBank debit`, `Cash`), 1–100 chars                  |
| spend time    | `spent_at`     | `TEXT` RFC 3339 with offset as entered + `spent_at_unix INTEGER`        |
| note          | `note`         | `TEXT`, optional, up to 1000 chars                                     |
| created       | `created_at`   | `TEXT` RFC 3339 in UTC (server clock)                                  |
| last edited   | `updated_at`   | `TEXT` RFC 3339 in UTC, `NULL` (JSON `null`) until the expense is edited |

### Amounts: integer minor units (no floats)

The amount is parsed from its **decimal text** straight into an integer count of
the currency's minor unit, with no floating point anywhere: `"120.50"` THB is
stored as `12050`, `"1500"` JPY as `1500` (JPY has no minor unit), `"1.234"` KWD
as `1234`. The number of decimals per currency comes from the ISO 4217 table in
`internal/expense/currency.go`. Input with more decimals than the currency
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
Bangkok expense was entered at +07:00). The derived `spent_at_unix` column holds
the absolute instant and is used for ordering and filtering, so rows entered with
different offsets compare correctly.

The web form has a date-time field plus a separate UTC-offset field. For a new
expense both default to the browser's current local time and offset, and the
offset follows the chosen date (DST-aware) until you type your own (for example
`+07:00` for a purchase in Bangkok). When you edit an expense, the form shows the
saved wall-clock time **in its original offset**, so an expense saved as
`09:15+07:00` appears as 09:15 with `+07:00` and is not converted to your
browser's zone.

### Schema migrations

The schema version is tracked in SQLite's `PRAGMA user_version`. The code is in
`internal/store/migrate.go`.

- **New, empty database:** the latest schema (`transactions` table + index) is
  created directly at the current version, in one SQL transaction.
- **Existing database:** pending migrations run in order. Each migration and
  its `user_version` bump share one SQL transaction, so an interrupted upgrade
  rolls back to the previous version and runs again on the next start.
  Migrations are also written to be idempotent, as a second line of defence.

| Version | Change |
|---------|--------|
| 0       | original schema: table `expenses`, index `idx_expenses_spent_at_unix` |
| 1       | `ALTER TABLE expenses ADD COLUMN updated_at TEXT` (NULL for existing rows) |
| 2       | `ALTER TABLE expenses RENAME TO transactions`, then `DROP INDEX IF EXISTS idx_expenses_spent_at_unix` and `CREATE INDEX IF NOT EXISTS idx_transactions_spent_at_unix ON transactions (spent_at_unix DESC, id DESC)` |

Notes on version 2:

- `RENAME TO` keeps every row, the AUTOINCREMENT counter (deleted IDs are still
  not reused) and attached indexes.
- SQLite cannot rename an index, so the old one is dropped and recreated
  under the new name.
- If the table is already called `transactions` (e.g. renamed by hand), only
  the index is fixed.
- If **both** `expenses` and `transactions` exist, startup fails with a clear
  error and nothing is changed. The tool won't guess which table holds the data.

Version 1 checks `pragma_table_info` before adding the column. A database with a
newer version than the binary supports is refused rather than modified. Each
step is logged (`store: migrated schema to version N`, or `store: created new
database at schema version N`). To be extra safe, stop the server and copy
`data/expenses.db` (the file name is unchanged) to `data/backups/` before
upgrading.

## API

All responses are JSON. Every error looks like
`{"error": "message", "fields": {"field": "problem", ...}}`. `fields` appears
only on validation errors.

| Method | Path                 | Success | Notes |
|--------|----------------------|---------|-------|
| POST   | `/api/expenses`      | 201 + expense, `Location` header | 422 on validation errors, 400 on malformed JSON or unknown fields |
| GET    | `/api/expenses`      | 200 `{"expenses":[...],"count":n}` | Newest first by spend time. Optional `from`, `to` (RFC 3339 with offset, both inclusive), `limit` (1–5000, default 500) |
| GET    | `/api/expenses/:id`  | 200 + expense | 404 if missing, 400 if id is not a positive integer |
| PUT    | `/api/expenses/:id`  | 200 + updated expense | Full replace with the same body and rules as POST. An omitted `note` clears it. 404 if missing, 422 for invalid values, 400 for malformed JSON or unknown fields |
| PATCH  | `/api/expenses/:id`  | 200 + updated expense | Partial update: only the fields you send change, then the merged result is validated like a create. `"note": null` clears the note; `null` for any other field returns 422. Returns 400 for `{}`, unknown fields or wrong JSON types, and 404 if missing |
| DELETE | `/api/expenses/:id`  | 204 | 404 if missing |
| GET    | `/api/healthz`       | 200 `{"status":"ok"}` | |
| GET    | `/`                  | HTML page | Static assets under `/static/` |

### Examples

```bash
curl -s -X POST http://127.0.0.1:8080/api/expenses \
  -H 'Content-Type: application/json' \
  -d '{"amount":"120.50","currency":"THB","account":"KBank debit","spent_at":"2026-10-06T21:06:00+08:00","note":"Lunch"}'
```

```json
{"id":1,"amount":"120.50","amount_minor":12050,"currency":"THB","account":"KBank debit",
 "spent_at":"2026-10-06T21:06:00+08:00","note":"Lunch","created_at":"2026-10-06T13:20:11Z"}
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
curl -s -X PUT http://127.0.0.1:8080/api/expenses/1 -H 'Content-Type: application/json' \
  -d '{"amount":"150","currency":"THB","account":"KBank debit","spent_at":"2026-10-05T09:15:00+07:00","note":"pad thai + drink"}'
# 200 {"id":1,"amount":"150.00","amount_minor":15000,"currency":"THB",...,"updated_at":"2026-10-06T13:18:11Z"}

curl -s -X PATCH http://127.0.0.1:8080/api/expenses/2 -H 'Content-Type: application/json' \
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

List with a time window (URL-encode the `+` as `%2B`; an unencoded `+` is also
accepted):

```bash
curl -s 'http://127.0.0.1:8080/api/expenses?from=2026-10-01T00:00:00%2B08:00&to=2026-10-31T23:59:59%2B08:00'
```

## Layout

```
cmd/server/main.go           entry point: flags, open DB, HTTP server, graceful shutdown
internal/expense/            domain model + validation (no I/O)
  expense.go                 Expense, CreateInput, Validate, ParseTimestamp
  patch.go                   PATCH merge (ApplyPatch), RequestError
  amount.go                  decimal string <-> integer minor units
  currency.go                ISO 4217 codes -> minor-unit digits
  expense_test.go, patch_test.go
internal/store/              persistence: SQLite table "transactions" (modernc.org/sqlite)
  store.go                   Store: Open, Create, List, Get, Update, Delete
  migrate.go                 current schema, versioned migrations (v0 -> v1 -> v2)
  store_test.go              fresh DB, v0/v1 -> v2 upgrades, partial/ambiguous states
internal/api/                Gin routes and handlers
  api.go
  api_test.go                create/list/get/delete
  edit_test.go               PUT/PATCH
web/embed.go                 embeds web/static into the binary
web/static/                  index.html, app.js, style.css (no build step)
```

## Web page

- A form to log an expense, and a table listing expenses newest first, with a
  date filter and totals per currency.
- **Edit** loads the expense into the same form: the card is highlighted, the
  heading reads "Edit expense #N", and the button reads "Save changes". Saving
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
- Protection against lost updates if two tabs edit the same expense at once
  (e.g. require `updated_at` to match, or `If-Match`/ETag). Today the last save wins.
- Whether payment accounts should be a managed list instead of free text.
- Naming follow-up to the `transactions` table rename: should the HTTP API
  (`/api/expenses`, the JSON `expenses` key), the Go domain package
  (`internal/expense`), the UI wording and the default DB file name
  (`data/expenses.db`) also move to "transactions"? Does "transactions" mean
  income/transfers will be recorded too, which would need a type/sign field?
- Refunds or negative amounts (currently rejected; amounts must be > 0).
