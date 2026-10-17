# expensetrackr API reference (for agents)

Companion to [SKILL.md](SKILL.md). The examples assume the `BASE` / `JAR`
setup, the login and the `balance_uid` / `category_uid` helpers from
SKILL.md ("Step 1"), plus `curl` and `jq`. The authoritative
spec is `GET $BASE/api/openapi.yaml` (public; also `/api/openapi.json`,
Swagger UI at `/swagger/`), hand-written in `pkg/apidoc/openapi.yaml`.

Contents: [Server](#server) · [Auth](#auth) · [Conventions](#conventions) ·
[Balances](#balances) · [Categories](#categories) ·
[Transactions](#transactions) · [Currencies](#currencies) ·
[Exchange rates](#exchange-rates) · [Versions](#versions-etag--if-match) ·
[Error bodies](#error-bodies) · [Helper script](#helper-script-scriptsetrsh)

## Server

- Default base URL `http://127.0.0.1:8080` (server flag `-addr` /
  `EXPENSE_ADDR`). Point agents elsewhere with `EXPENSETRACKR_URL`.
- `GET /api/healthz` → `{"status":"ok"}` (no login) to check it is up.
- Data (transactions, balances) is **shared by all accounts** on the server;
  categories are per user.

## Auth

| call | body | result |
|---|---|---|
| `POST /api/auth/login` | `{"username","password"}` | 200 user + `Set-Cookie: session=…` (HttpOnly, SameSite=Lax, 30-day sliding expiry). 401 `invalid_credentials`; 429 `throttled` after 5 failures (username + IP) within 15 min, locked 5 min, `Retry-After` |
| `GET /api/auth/me` | – | 200 user (`uid, username, display_name, …`); 401 `unauthenticated` / `setup_required` |
| `POST /api/auth/logout` | none | 204, session deleted, cookie cleared |
| `POST /api/auth/register` | `{"username","password","display_name"?}` | first run only (or when logged in): 201. Not for agents unless asked |
| `PUT /api/auth/password` | `{"current_password","new_password"}` | 204; logs out other sessions. Not for agents unless asked |

```bash
curl -sS -b "$JAR" -c "$JAR" "$BASE/api/auth/me" | jq '{username, display_name}'
```

Public without a session: login, register, logout, healthz, the OpenAPI
documents. Everything else is 401 without a valid cookie.

CSRF guard (POST/PUT/DELETE/PATCH on `/api/*`), in this order:

1. `Sec-Fetch-Site`, if present, must be `same-origin` or `none` → else 403
   `{"error":"cross-site request rejected","code":"csrf"}`.
2. `Origin`, if present, must have host:port equal to the request's `Host`
   (`null` rejected) → else 403 `{"error":"cross-origin request rejected","code":"csrf"}`.
3. A request with a body must be `Content-Type: application/json` (charset
   parameter allowed) → else 415.

curl, Python `requests` and similar send neither header, so they pass. A
browser-like client that adds `Origin` must use the same host spelling as the
URL (`http://127.0.0.1:8080` ≠ `http://localhost:8080`).

## Conventions

- **uids**: lowercase UUID v4, server-assigned. Path uids that are not
  UUID-shaped are 400; unknown ones 404. A `uid` in a POST/PUT body is ignored.
- **Amounts**: send decimal strings. Responses carry the decimal string and
  integer minor units (`amount` + `amount_minor`, `balance` + `balance_minor`, …).
  Decimals per currency come from ISO 4217 (`GET /api/currencies`, `scale`):
  more decimals than allowed is 422, never rounded; trailing zeros are fine.
- **Times**: `spent_at` RFC 3339 with offset, stored to the second with the
  offset as entered. `created_at` / `updated_at` are UTC; `updated_at` is
  `null` until edited.
- **Writes**: unknown fields → 400 (`unknown field "amount_minor"`), so send
  only input fields. PUT is a full replace; there is no PATCH for
  transactions, balances or categories (404).

## Balances

Response fields: `uid, name, type, kind (asset|liability), currency,
description, balance, balance_minor, debt, debt_minor, limit, limit_minor,
available, available_minor, over_limit, created_at, updated_at, version`.
Fields a type doesn't use are `null`.

| type | input amounts | notes |
|---|---|---|
| `payment_account` | `balance` (may be ≤ 0) | can pay expenses, receive income |
| `other_asset` | `balance` | receives income, asset purchases, transfers |
| `credit_card` | `debt`, `limit` (≥ 0) | can pay expenses; `available = limit − debt`, `over_limit` |
| `other_liability` | `debt`, `limit` | loans; only transfers in/out |

```bash
# List (grouped by type, then name) + per-currency totals
curl -sS -b "$JAR" -c "$JAR" "$BASE/api/balances" | jq '.totals'
# Only accounts that can pay an expense
curl -sS -b "$JAR" -c "$JAR" "$BASE/api/balances?payable=1" | jq -r '.balances[].name'
# Only one type
curl -sS -b "$JAR" -c "$JAR" "$BASE/api/balances?type=credit_card" | jq -r '.balances[] | "\(.name) \(.debt)/\(.limit) \(.currency)"'
```

`totals[]`: `{currency, assets, liabilities, net, credit_limit, available_credit}`
(no cross-currency conversion). `payable` and `type` can't be combined (400).

Create (only when the user asks for a new account; names are unique,
case-insensitively, 409 otherwise):

```bash
curl -sS -b "$JAR" -c "$JAR" -X POST "$BASE/api/balances" -H 'Content-Type: application/json' \
  -d '{"name":"Savings","type":"other_asset","currency":"THB","balance":"0","description":"Emergency fund"}' \
  | jq '{uid, name, type, balance, version}'
SAVINGS_UID=$(balance_uid Savings)
```

Edit (manual override; `type` is required and must stay the same, 422
otherwise). A change of `balance` / `debt` records a `balance_adjustment`
transaction with the difference; limit-only or description changes record
nothing:

```bash
B=$(curl -sS -b "$JAR" -c "$JAR" "$BASE/api/balances/$SAVINGS_UID")
echo "$B" | jq '{name, type, currency, description, balance, debt, limit}
                | with_entries(select(.value != null)) | .balance = "2500"' \
  | curl -sS -b "$JAR" -c "$JAR" -X PUT "$BASE/api/balances/$SAVINGS_UID" \
      -H 'Content-Type: application/json' -H "If-Match: \"$(echo "$B" | jq .version)\"" \
      --data-binary @- \
  | jq '{name, balance, version}'
curl -sS -b "$JAR" -c "$JAR" "$BASE/api/transactions?type=balance_adjustment&limit=1" \
  | jq '.transactions[0] | {type, amount, adjustment_direction, account, note}'
```

Changing `currency` is 409 `balance_in_use` while any transaction references
the balance. `DELETE /api/balances/{uid}` (optional `If-Match`) → 204;
transactions keep their uid and name snapshot, so confirm first and prefer
not to delete accounts with history.

## Categories

Per logged-in user. Fields: `uid, name (≤ 100), type (expense|income),
description (≤ 1000), created_at, updated_at`. Names unique per type,
case-insensitively (409). Max 500 per user. **Not versioned**: PUT needs no
`If-Match`.

```bash
curl -sS -b "$JAR" -c "$JAR" "$BASE/api/categories?type=income" | jq -r '.categories[].name'
curl -sS -b "$JAR" -c "$JAR" -X POST "$BASE/api/categories" -H 'Content-Type: application/json' \
  -d '{"name":"Transport","type":"expense","description":"Taxi, BTS, fuel"}' | jq '{uid, name, type}'
TRANSPORT_UID=$(category_uid Transport expense)
# Rename (full replace: omitted description is cleared)
curl -sS -b "$JAR" -c "$JAR" -X PUT "$BASE/api/categories/$TRANSPORT_UID" -H 'Content-Type: application/json' \
  -d '{"name":"Travel","type":"expense","description":"Taxi, BTS, fuel"}' | jq '{name, updated_at}'
curl -sS -b "$JAR" -c "$JAR" -o /dev/null -w '%{http_code}\n' -X DELETE "$BASE/api/categories/$TRANSPORT_UID"   # 204
```

DELETE or a type change of a category used by any transaction is 409; clear
or change `category_uid` on those transactions first (ask the user).

## Transactions

Response fields: `uid, type, amount, amount_minor, currency, balance_uid,
account (source name snapshot), to_balance_uid, to_account, category_uid,
spent_at, note, created_at, updated_at, version, adjusts_balances,
adjustment_direction, to_amount, to_amount_minor, to_currency,
exchange_rate, rate_source, rate_date`.

**Input fields** (POST and PUT): `type, amount, currency, balance_uid,
to_balance_uid, category_uid, spent_at, note, to_amount, to_currency`
(+ optional `version` instead of `If-Match` on PUT).

Balance effects (automatic):

| type | source (`balance_uid`) | destination (`to_balance_uid`) |
|---|---|---|
| expense | asset −amount / card debt +amount | – |
| income | asset +amount | – |
| transfer | asset −amount / liability debt +amount | asset +to_amount / liability debt −to_amount (card payment, loan repayment) |
| asset_purchase | like expense | `other_asset` +amount (same currency required) |
| balance_adjustment | none (server-created record of a manual edit) | – |

`adjusts_balances` is `false` for rows created before automatic balances
existed; editing/deleting those doesn't move balances.

### List: `GET /api/transactions`

| param | meaning |
|---|---|
| `from`, `to` | RFC 3339 with offset, inclusive; encode `+` as `%2B` (a raw `+` also works) |
| `type` | `expense`, `income`, `transfer`, `asset_purchase`, `balance_adjustment` |
| `category_uid` | one category |
| `limit` | 1–5000, default 500 |

Newest first by `spent_at` instant. Response `{"transactions":[…],"count":n}`.
Invalid params → 400 with `fields`. No balance filter and no pagination
cursor: filter by `balance_uid` client-side and narrow `from`/`to` when
`count` hits `limit`.

```bash
# Spending per currency in October (expenses only)
curl -sS -b "$JAR" -c "$JAR" \
  "$BASE/api/transactions?type=expense&from=2026-10-01T00:00:00%2B08:00&to=2026-10-31T23:59:59%2B08:00" \
  | jq 'reduce .transactions[] as $t ({}; .[$t.currency] += $t.amount_minor)'
# → minor units, e.g. {"THB":12050} = 120.50 THB (divide by 10^scale)

# Spending by category name
CATS=$(curl -sS -b "$JAR" -c "$JAR" "$BASE/api/categories")
curl -sS -b "$JAR" -c "$JAR" "$BASE/api/transactions?type=expense" \
  | jq --argjson cats "$CATS" '($cats.categories | map({(.uid): .name}) | add // {}) as $n
       | [.transactions[] | {cat: ($n[.category_uid // ""] // "(none)"), currency, amount_minor}]
       | group_by(.cat, .currency) | map({category: .[0].cat, currency: .[0].currency, minor: (map(.amount_minor) | add)})'
```

### Get one: `GET /api/transactions/{uid}` → 200 + `ETag`

### Replace: `PUT /api/transactions/{uid}`

Same body rules as POST, plus the version (SKILL.md "Edit"). Notes:

- Omitted optional fields are cleared: `note`, `category_uid`,
  `to_balance_uid`. Omitted `type` means `expense`, so always send `type`.
- Changing `type` away from transfer/asset_purchase: drop `to_balance_uid`;
  to expense/income: drop or replace `category_uid` with one of the new type.
- Cross-currency transfer: omitting `to_amount` keeps the stored conversion if
  amount, currency, both balances and the `spent_at` date are unchanged;
  otherwise the rate is fetched again. Sending `to_amount` makes it manual.
  Only send back `to_amount` when `rate_source` was `manual`.
- The old balance effect is reversed and the new one applied atomically.

### Delete: `DELETE /api/transactions/{uid}` → 204 (optional `If-Match`)

Reverses the balance effect. `balance_adjustment` rows → 409
`balance_adjustment_readonly`.

### Same-currency transfer with `to_amount`

`to_amount` may be omitted or must equal `amount` (422 otherwise). For a
cross-currency transfer with a known received amount (manual rate):

```bash
curl -sS -b "$JAR" -c "$JAR" -X POST "$BASE/api/transactions" -H 'Content-Type: application/json' \
  -d "$(jq -n --arg b "$USD_UID" --arg t "$WALLET_UID" \
        '{type:"transfer", amount:"200", currency:"USD", balance_uid:$b, to_balance_uid:$t,
          to_amount:"6650", spent_at:"2026-10-13T10:00:00+08:00", note:"Wise transfer"}')" \
  | jq '{amount, currency, to_amount, to_currency, exchange_rate, rate_source, rate_date}'
```

## Currencies

```bash
curl -sS -b "$JAR" -c "$JAR" "$BASE/api/currencies" | jq -c '.count, (.currencies[] | select(.code == "THB" or .code == "JPY"))'
```

`{"currencies":[{"code","name","scale"}],"count":n}`, sorted by code. `scale`
is the number of decimals allowed in amounts.

## Exchange rates

`GET /api/exchange-rates?from=USD&to=THB[&date=YYYY-MM-DD]` (login required)
→ `{"from","to","rate","date","source"}`; `rate` = `to` units per 1 `from`.
No `date` = latest. The ECB publishes on working days only, so `date` in the
answer may be earlier than asked. `from == to` → rate `"1"`, source
`same_currency`. Errors: 400 bad query, 422 unsupported pair
(`exchange_rate_unsupported`), 503/502 `exchange_rate_unavailable`. It is a
preview only; writes fetch their own rate.

```bash
curl -sS -b "$JAR" -c "$JAR" "$BASE/api/exchange-rates?from=USD&to=THB&date=2026-10-12" | jq -c .
curl -sS -b "$JAR" -c "$JAR" "$BASE/api/exchange-rates?from=USD&to=VND" | jq -c .
```

Rates are ECB mid-market reference rates for ~30 currencies, not what a bank
charged; use `to_amount` when the user knows the received amount.

## Versions (ETag / If-Match)

- Balances and transactions: `version` starts at 1 and goes up on every
  change. A balance's version also goes up whenever a transaction moves it.
- `ETag: "<version>"` on POST, GET and PUT of one record (lists carry
  `version` in each item).
- PUT: `If-Match: "<n>"` (or `W/"<n>"`) or body `"version": n`; both must agree
  if both sent. Missing → 428; malformed → 400; stale → 409 with `current`
  (the record now) and its ETag. The version is checked before validation.
- DELETE: optional `If-Match`; stale → 409.

```bash
curl -sS -b "$JAR" -c "$JAR" -D - -o /dev/null "$BASE/api/balances/$WALLET_UID" | grep -i '^etag'
```

## Error bodies

Real responses (from a test server):

```text
400 {"error":"unknown field \"amount_minor\""}
400 {"error":"uid must be a UUID (xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx)"}
400 {"error":"invalid query parameters","fields":{"type":"must be one of: expense, income, transfer, asset_purchase, balance_adjustment"}}
401 {"error":"authentication required","code":"unauthenticated"}
403 {"error":"cross-origin request rejected","code":"csrf"}
404 {"error":"transaction not found"}
409 {"error":"version conflict: this transaction was changed since you read it (now at version 2); …","code":"version_conflict","fields":{"version":"is stale; the current version is 2"},"current":{…}}
415 {"error":"Content-Type must be application/json"}
422 {"error":"validation failed","fields":{"balance_uid":"balance \"Wallet\" is in THB but the transaction is in USD; the currencies must match"}}
428 {"error":"this update needs the version it is based on: …","code":"version_required"}
429 {"error":"too many failed attempts; try again later","code":"throttled"}   (header Retry-After: 300)
503 {"error":"the exchange-rate service (Frankfurter) is unavailable; try again later or provide to_amount (the amount received) manually","code":"exchange_rate_unavailable"}
```

```bash
# 422: currency must match the balance
curl -sS -b "$JAR" -c "$JAR" -X POST "$BASE/api/transactions" -H 'Content-Type: application/json' \
  -d "$(jq -n --arg b "$WALLET_UID" '{amount:"10", currency:"USD", balance_uid:$b, spent_at:"2026-10-06T12:00:00+08:00"}')"
# 422: no offset in spent_at, too many decimals for JPY... (fields lists each problem)
curl -sS -b "$JAR" -c "$JAR" -X POST "$BASE/api/transactions" -H 'Content-Type: application/json' \
  -d "$(jq -n --arg b "$WALLET_UID" '{amount:"10.5", currency:"JPY", balance_uid:$b, spent_at:"2026-10-06T12:00:00"}')"
```

## Helper script: `scripts/etr.sh`

Bash + curl + jq. Env: `EXPENSETRACKR_URL`, `EXPENSETRACKR_USERNAME`,
`EXPENSETRACKR_PASSWORD`, `EXPENSETRACKR_COOKIE_JAR` (default
`~/.cache/expensetrackr/cookies.txt`, mode 600). Body on stdout,
`HTTP <status> [ETag "<n>"]` on stderr; exit 0 for 2xx, 1 for other HTTP
statuses, 2 for usage/network/login errors. It logs in when needed (and once
more after a 401), sends JSON with the right content type, never sends
`Origin` / `Sec-Fetch-Site`, and passes the password to curl via a temp file,
not argv.

```bash
ETR=skills/expensetrackr-api/scripts/etr.sh   # path relative to the repo root
$ETR GET /api/balances | jq -r '.balances[].name'
$ETR POST /api/transactions "$(jq -n --arg b "$WALLET_UID" \
  '{amount:"45", currency:"THB", balance_uid:$b, spent_at:"2026-10-09T08:15:00+08:00", note:"Coffee"}')" \
  | jq -r .uid > /tmp/etr-coffee-uid
# edit = GET, keep editable fields, apply a jq filter, PUT with If-Match, retry on 409
$ETR edit "/api/transactions/$(cat /tmp/etr-coffee-uid)" '.note = "Coffee + croissant" | .amount = "95"' | jq '{note, amount, version}'
$ETR DELETE "/api/transactions/$(cat /tmp/etr-coffee-uid)" --if-match 2
$ETR logout
```

`PUT` takes `--if-match N` when you build the body yourself; `edit` works for
`/api/transactions/{uid}`, `/api/balances/{uid}` and `/api/categories/{uid}`.
