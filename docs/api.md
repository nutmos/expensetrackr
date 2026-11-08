# API

[← Back to the README](../README.md) · [All docs](README.md)

Every `/api` endpoint with its responses, then worked `curl` examples. The
authoritative spec is [`openapi.yaml`](../pkg/apidoc/openapi.yaml), served
live at `/swagger/` (see [the docs index](README.md#api-documentation)).

**Authentication** ([docs/auth.md](auth.md)): every `/api` route needs a
session cookie (from `POST /api/auth/login`) or an API token
(`Authorization: Bearer ext_...`) except login, register (first run
only), logout, `/api/healthz` and the OpenAPI documents; otherwise **401**
`{"error":"authentication required","code":"unauthenticated"}`.
State-changing requests must be same-origin (`Origin` / `Sec-Fetch-Site`,
**403** otherwise) and send JSON bodies as `application/json` (**415**).

Resources are identified **only by `uid`** (UUID v4) in URLs and JSON;
the internal integer row id is never returned or accepted. `Location`
headers on create are `/api/transactions/<uid>` and `/api/balances/<uid>`.

All responses are JSON. Every error looks like
`{"error": "message", "fields": {"field": "problem", ...}}`. `fields` appears
only on validation and conflict errors.
- Some errors also carry a machine-readable `"code"`: `version_required`,
  `version_conflict` (with `"current"`, the record as it is now),
  `balance_in_use`, or `balance_adjustment_readonly`.

**Versioning** applies to balances and transactions (details in
[docs/balances.md](balances.md#3-optimistic-locking-versions)):
- Responses carry `"version"` and `ETag: "<version>"`.
- PUT **must** send `If-Match: "<version>"` (preferred) or
  `"version": <n>` in the body. Neither gives **428**, a stale version gives
  **409** with the current record, and both checks come before 422.
- DELETE accepts an optional `If-Match`.

| Method | Path                 | Success | Notes |
|--------|----------------------|---------|-------|
| POST   | `/api/transactions`      | 201 + transaction, `Location` + `ETag` headers | Moves the balance(s) in the same DB transaction. 422 on validation errors (incl. currency ≠ balance currency, `type` `balance_adjustment`), 400 on malformed JSON or unknown fields. Optional `import_ref` (≤ 200 chars, unique per balance; 409 `duplicate_import_ref`) |
| POST   | `/api/transactions/bulk` | 201 / 200 `{"created","skipped","failed","valid","dry_run","atomic","written","results":[...],"balances":[...]}` | Up to 500 items with the POST body shape (+ `import_ref`); `atomic` (default true: 422 and nothing written if any item fails), `dry_run`. Duplicate `import_ref` per balance → `skipped`. 413 over 500 items, 2 MiB body. See [docs/bulk-import.md](bulk-import.md) |
| GET    | `/api/transactions`      | 200 `{"transactions":[...],"count":n,"next_cursor":...}` | Newest first by spend time. Optional `from`, `to` (RFC 3339 with offset, both inclusive), `limit` (1–5000 per page, default 500), `type` (`expense`/`income`/`transfer`/`asset_purchase`/`return_on_investment`/`balance_adjustment`; 400 if invalid), `category_uid` (UUID; 400 if malformed), `balance_uid` (UUID; source or destination). **Paged:** while `next_cursor` is not null, send it back as `cursor` with the same filters for the next page (keyset pagination, stable under inserts and deletes) |
| GET    | `/api/transactions/:uid`  | 200 + transaction | `:uid` is the transaction UUID (case-insensitive). **400** if it is not UUID-shaped (including numeric ids such as `/api/transactions/1`), **404** if no such uid. Same for PUT/DELETE |
| PUT    | `/api/transactions/:uid`  | 200 + updated transaction | Full replace with the same body and rules as POST. An omitted `note` clears it; an omitted `import_ref` is kept (`""` clears it). Requires `If-Match` or `"version"` (428 / 409). Reverses the old balance effect and applies the new one. 409 `balance_adjustment_readonly` on a balance adjustment (also DELETE). 404 if missing, 422 for invalid values, 400 for malformed JSON or unknown fields |
| DELETE | `/api/transactions/:uid`  | 204 | Reverses the balance effect. Optional `If-Match` (409 if stale). 404 if missing |
| POST   | `/api/balances`      | 201 + balance, `Location` header | 422 on validation errors (incl. per-type amount rules), 400 on malformed JSON or unknown fields. Names need not be unique |
| GET    | `/api/balances`      | 200 `{"balances":[...],"count":n,"totals":[...]}` | Grouped by type, then by name. Optional `type=` filter (400 if invalid). Optional `payable=1` / `payable=true` returns only `payment_account` and `credit_card` (cannot combine with `type=`). `totals` covers the returned rows, per currency |
| GET    | `/api/balances/:uid`  | 200 + balance | `:uid` is the balance UUID. 400 if not UUID-shaped (numeric ids are no longer accepted), 404 if missing. Same for PUT/DELETE |
| PUT    | `/api/balances/:uid`  | 200 + updated balance | Full replace (manual override of the amounts; a balance/debt change records a `balance_adjustment` transaction). `type` is required and must equal the stored type (422 on `type` otherwise). `uid` in the body is ignored. Requires `If-Match` or `"version"` (428 / 409). 409 `balance_in_use` for a currency change while transactions reference it. Returns 404, 409, 422 or 400 as for POST |
| DELETE | `/api/balances/:uid`  | 204 | Optional `If-Match` (409 if stale). 404 if missing |
| POST   | `/api/categories`      | 201 + category, `Location: /api/categories/<uid>` | Adds to the logged-in user's categories. 422 on validation errors, 409 duplicate name within the type or 500-category limit reached, 400 malformed JSON / unknown fields |
| GET    | `/api/categories`      | 200 `{"categories":[...],"count":n}` | The logged-in user's categories. Expense first, then income, by name. Optional `type=expense\|income` (400 if invalid) |
| GET    | `/api/categories/:uid` | 200 + category | 400 if not UUID-shaped, 404 if missing or another user's |
| PUT    | `/api/categories/:uid` | 200 + updated category | Full replace (omitted description clears it). 409 duplicate name, or type change while referenced by transactions |
| DELETE | `/api/categories/:uid` | 204 | 409 if any transaction references it; 404 if missing |
| POST   | `/api/auth/register`   | 201 + user | `username`, `password`, optional `display_name`. Anyone on first run (also logs in); afterwards only when logged in (403 `registration_closed`) |
| POST   | `/api/auth/login`      | 200 + user, `Set-Cookie: session=…` | Generic 401 `invalid username or password`; 429 after 5 failures (username + IP) for 5 min |
| POST   | `/api/auth/logout`     | 204 | Deletes the session, clears the cookie |
| GET    | `/api/auth/me`         | 200 + user | 401 when signed out (`code: setup_required` before the first account exists) |
| PUT    | `/api/auth/password`   | 204 | `current_password`, `new_password`; logs out all other sessions |
| GET    | `/api/tokens`          | 200 `{"tokens":[...],"count":n}` | Session only. Your active API tokens (display prefix, never the secret) |
| POST   | `/api/tokens`          | 201 + token incl. `token` (shown once) | Session only. `name` (≤ 64 chars), `scopes` (`read`, `write`), optional `expires_in_days` (1–3650); 422 invalid |
| DELETE | `/api/tokens/:uid`     | 204 | Session only. Revokes; 404 not yours / unknown |
| POST   | `/api/users`           | 403 `use_register` | Disabled: create accounts with `POST /api/auth/register` |
| GET    | `/api/users`           | 200 `{"users":[...],"count":1}` | Only your own account |
| GET    | `/api/users/:uid`      | 200 + user | **Your own uid only**: any other uid, existing or not, is 404 (all `/api/users/:uid…` routes). Never includes `password_hash`; `has_password` instead |
| PUT    | `/api/users/:uid`      | 200 + updated user | Full replace of profile fields (omitted username/email removed, preferences → defaults). Read-only fields in the body are ignored. A user with a password keeps their username: 422 `username_required` |
| PATCH  | `/api/users/:uid`      | 200 + updated user | Partial; `preferences` **replaces** the whole object (`null` → defaults); `username`/`email` `null` removes (422 `username_required` for the username of a user with a password) |
| DELETE | `/api/users/:uid`      | 204 | Deletes your account (and its identities, sessions, tokens and data). 409 `last_login_user` for the last user with a password, so first-run registration never reopens. PUT, PATCH and DELETE need a browser session (API token: 403 `session_required`) |
| GET    | `/api/users/:uid/identities` | 200 `{"identities":[...],"count":n}` | Read-only SSO links (empty until SSO exists) |
| GET    | `/api/users/:uid/preferences` | 200 `{"base_currency":"USD"}` | Your own preferences (defaults filled in); another user's uid is 404 |
| PUT    | `/api/users/:uid/preferences` | 200 + preferences | Replaces the whole object (omitted keys → defaults). Own uid only; unsupported `base_currency` or unknown key is a 400 |
| GET    | `/api/currencies`    | 200 `{"currencies":[{"code":"THB","name":"Baht","scale":2,"flag":"🇹🇭"},...],"count":n}` | Supported ISO 4217 codes (from `pkg/money`), sorted by code, with a display-only flag emoji (see below); the web page's currency dropdowns use it |
| GET    | `/api/exchange-rates?from=USD&to=THB&date=YYYY-MM-DD` | 200 `{"from":"USD","to":"THB","rate":"33.615","date":"2026-10-12","source":"frankfurter"}` | Rate preview (cached Frankfurter / ECB rate; `date` optional = latest). 400 bad query, 422 unsupported pair, 503/502 Frankfurter down. See [docs/exchange-rates.md](exchange-rates.md) |
| GET    | `/api/healthz`       | 200 `{"status":"ok"}` | |
| GET    | `/api/openapi.yaml`  | 200 the OpenAPI 3.0 document | Hand-written spec, embedded in the binary |
| GET    | `/api/openapi.json`  | 200 the same document as JSON | |
| GET    | `/swagger/`, `/swagger` | Swagger UI (HTML) | `/swagger` redirects to `/swagger/`. Assets are vendored and embedded (offline; no CDN) |
| GET    | `/`, `/transactions`, `/balances`, `/categories`, `/settings` | HTML page (list) | Same `index.html` for every page path; the client-side router picks the view. Static assets under `/static/` |
| GET    | `/favicon.ico` | 204 | No icon; answered so browsers do not log a 404 |
| GET    | `/<res>/new`, `/<res>/:uid/edit` | HTML page (add / edit form) | `<res>` = `transactions`, `balances` or `categories`. `:uid` must be UUID-shaped (any case), else 404; an unknown uid is reported by the page itself. Other paths → 404 JSON |

## Examples

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
