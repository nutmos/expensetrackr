---
name: expensetrackr-api
description: Use the expensetrackr HTTP JSON API to log expenses, income, transfers (including cross-currency) and asset purchases, check account balances, list or filter transactions, and manage categories in a running expensetrackr server. Use it whenever the user asks to record, look up, correct or delete their money movements or balances in expensetrackr.
---

# expensetrackr API

expensetrackr is a small personal-finance server (Go + SQLite). Everything an
agent needs is a JSON API under `/api`. Field-level details, every query
parameter and more examples are in [reference.md](reference.md); the live,
authoritative spec is `GET /api/openapi.yaml` (no login needed).

## Safety rules (read first)

- **Never invent data.** Amounts, currencies, accounts, categories and dates
  come from the user or from the API. If the user did not say which account
  paid, or the amount is unclear, ask. Don't guess a time zone either: use
  the user's offset (ask if unknown).
- **Confirm before destructive or bulk changes**: any DELETE, editing a
  balance amount, or changing more than one record. Say what will change in
  human terms ("delete the 120.50 THB lunch on 6 Oct from Wallet?").
- **Never create, edit or delete `balance_adjustment` transactions.** The
  server records them itself when a balance amount is edited (POST/PUT with
  that type is 422, PUT/DELETE of one is 409).
- **Talk in names, not uids.** Use uids in API calls only; show users account
  and category names, amounts and dates unless they ask for ids.
- **Protect credentials.** Read them from `EXPENSETRACKR_USERNAME` /
  `EXPENSETRACKR_PASSWORD`; never echo, log, or put them in a command line
  argument or a file other than the cookie jar. Don't print the cookie jar.
- Don't use another user's account, and don't call `/api/users` or
  `/api/auth/register` / `/api/auth/password` unless the user asks.

## Setup

```bash
BASE="${EXPENSETRACKR_URL:-http://127.0.0.1:8080}"   # server base URL, no trailing slash
JAR="${EXPENSETRACKR_COOKIE_JAR:-$HOME/.cache/expensetrackr/cookies.txt}"
mkdir -p "$(dirname "$JAR")" && touch "$JAR" && chmod 600 "$JAR"
```

The examples need `curl` and `jq`. Instead of raw curl you can use the helper
[scripts/etr.sh](scripts/etr.sh) (same env vars; logs in automatically,
re-logs in after a 401, and does GET → edit → PUT with `If-Match` and
conflict retry): `scripts/etr.sh GET /api/balances`,
`scripts/etr.sh edit /api/transactions/$TX_UID '.note = "Lunch"'`.

## Authentication and request rules

Log in once; the server answers with a `session` cookie (HttpOnly, 30 days,
sliding) that curl keeps in the jar. The password goes through stdin, not argv:

```bash
jq -n '{username: env.EXPENSETRACKR_USERNAME, password: env.EXPENSETRACKR_PASSWORD}' \
  | curl -sS -c "$JAR" -X POST "$BASE/api/auth/login" \
      -H 'Content-Type: application/json' --data-binary @- \
  | jq '{username, display_name}'
```

Then send `-b "$JAR" -c "$JAR"` on every call. Rules every request must follow:

- **401** `{"code":"unauthenticated"}` → the session is missing or expired: log
  in again and retry once. `code: setup_required` means no account exists yet
  (a human must create one). Login failures are a generic 401
  `invalid_credentials`; after 5 failures in 15 min the username + IP is
  locked for 5 min (**429**, `Retry-After` header). Don't retry wrong
  credentials in a loop — stop and tell the user.
- **Bodies must be `Content-Type: application/json`** (415 otherwise; note
  that plain `curl -d` sends `application/x-www-form-urlencoded`).
- **CSRF guard on POST/PUT/DELETE**: a CLI client should send **no** `Origin`
  and **no** `Sec-Fetch-Site` header (curl sends neither). If your HTTP
  client adds them, `Origin` must be exactly the server's scheme+host+port as
  in the request's `Host` (e.g. `http://127.0.0.1:8080`, not `localhost`), and
  `Sec-Fetch-Site` must be `same-origin` or `none`; anything else is **403**
  `code: csrf`. GET requests are not checked.
- Unknown JSON fields are rejected (**400**), so never send a GET response
  back as a PUT body; send only the editable fields (see "Edit").

## Step 1: look up uids (always first)

Transactions reference balances and categories by `uid`. Fetch them, then
pick by name (case-insensitive). The names below (Wallet, Credit Card,
USD Account, Laptop, Food) are examples; use the user's.

```bash
curl -sS -b "$JAR" -c "$JAR" "$BASE/api/balances" \
  | jq -r '.balances[] | [.uid, .name, .type, .currency, (.balance // .debt)] | @tsv'
curl -sS -b "$JAR" -c "$JAR" "$BASE/api/categories" \
  | jq -r '.categories[] | [.uid, .name, .type] | @tsv'

# uid by name (case-insensitive); empty output = no such name
balance_uid() { curl -sS -b "$JAR" -c "$JAR" "$BASE/api/balances" \
  | jq -r --arg n "$1" '.balances[] | select((.name | ascii_downcase) == ($n | ascii_downcase)) | .uid'; }
category_uid() { curl -sS -b "$JAR" -c "$JAR" "$BASE/api/categories?type=$2" \
  | jq -r --arg n "$1" '.categories[] | select((.name | ascii_downcase) == ($n | ascii_downcase)) | .uid'; }
WALLET_UID=$(balance_uid Wallet); CARD_UID=$(balance_uid "Credit Card")
USD_UID=$(balance_uid "USD Account"); LAPTOP_UID=$(balance_uid Laptop)
FOOD_UID=$(category_uid Food expense)
```

Balance types: `payment_account`, `other_asset` (assets, field `balance`),
`credit_card`, `other_liability` (liabilities, fields `debt`, `limit`,
`available`). `GET /api/balances` also returns per-currency `totals`. If the
name matches nothing (or several), ask the user; don't create accounts unasked.

## Step 2: record a transaction

`POST /api/transactions` → **201** with the record (`uid`, `version`, `ETag`).
The balances move automatically in the same DB transaction.

| field | rule |
|---|---|
| `type` | `expense` (default), `income`, `transfer`, `asset_purchase` |
| `amount` | decimal **string**, > 0, at most the currency's decimals (`"120.50"` THB, `"1500"` JPY); never negative |
| `currency` | ISO 4217; **must equal the (source) balance's currency** (422 otherwise) |
| `balance_uid` | expense / asset_purchase: `payment_account` or `credit_card`; income: `payment_account` or `other_asset`; transfer: any |
| `to_balance_uid` | transfer (any other balance) and asset_purchase (an `other_asset`) only |
| `category_uid` | optional; expense and asset_purchase need an `expense` category, income an `income` one; none on transfers |
| `spent_at` | RFC 3339 **with offset**: `2026-10-06T21:06:00+08:00` (no date-only, no naive times) |
| `note` | optional, ≤ 1000 chars |

```bash
# Expense: 120.50 THB lunch paid from Wallet
TX=$(curl -sS -b "$JAR" -c "$JAR" -X POST "$BASE/api/transactions" -H 'Content-Type: application/json' \
  -d "$(jq -n --arg b "$WALLET_UID" --arg c "$FOOD_UID" \
        '{type:"expense", amount:"120.50", currency:"THB", balance_uid:$b, category_uid:$c,
          spent_at:"2026-10-06T12:30:00+08:00", note:"Lunch"}')")
echo "$TX" | jq '{type, amount, currency, account, spent_at, version}'
TX_UID=$(echo "$TX" | jq -r .uid)

# Income into Wallet (category optional)
curl -sS -b "$JAR" -c "$JAR" -X POST "$BASE/api/transactions" -H 'Content-Type: application/json' \
  -d "$(jq -n --arg b "$WALLET_UID" \
        '{type:"income", amount:"50000", currency:"THB", balance_uid:$b, spent_at:"2026-10-01T09:00:00+08:00", note:"Salary"}')" \
  | jq '{uid, type, amount, account}'

# Transfer: pay 5000 THB of the credit card from Wallet (same currency)
curl -sS -b "$JAR" -c "$JAR" -X POST "$BASE/api/transactions" -H 'Content-Type: application/json' \
  -d "$(jq -n --arg b "$WALLET_UID" --arg t "$CARD_UID" \
        '{type:"transfer", amount:"5000", currency:"THB", balance_uid:$b, to_balance_uid:$t, spent_at:"2026-10-07T22:00:00+08:00"}')" \
  | jq '{uid, type, amount, account, to_account}'

# Asset purchase: laptop bought on the credit card, tracked as an other_asset
curl -sS -b "$JAR" -c "$JAR" -X POST "$BASE/api/transactions" -H 'Content-Type: application/json' \
  -d "$(jq -n --arg b "$CARD_UID" --arg t "$LAPTOP_UID" \
        '{type:"asset_purchase", amount:"35000", currency:"THB", balance_uid:$b, to_balance_uid:$t, spent_at:"2026-10-08T15:00:00+08:00"}')" \
  | jq '{uid, type, amount, account, to_account}'
```

Use `jq -n --arg` (as above) to build bodies so names and notes are escaped
correctly. Read the response and confirm to the user in words, e.g. "Logged
120.50 THB for lunch from Wallet on 6 Oct."

### Cross-currency transfer

When source and destination balances have different currencies, `amount` /
`currency` are the **source** side. Then either:

- **omit `to_amount`**: the server converts with the ECB reference rate
  (Frankfurter) for the date of `spent_at` and stores `to_amount`,
  `exchange_rate`, `rate_source: "frankfurter"`, `rate_date`; or
- **send `to_amount`** (amount received, in the destination currency) when the
  user knows it (bank statement): `rate_source: "manual"`. Prefer this when
  the user gives the received amount; never make one up.

```bash
curl -sS -b "$JAR" -c "$JAR" -X POST "$BASE/api/transactions" -H 'Content-Type: application/json' \
  -d "$(jq -n --arg b "$USD_UID" --arg t "$WALLET_UID" \
        '{type:"transfer", amount:"100", currency:"USD", balance_uid:$b, to_balance_uid:$t, spent_at:"2026-10-12T10:00:00+08:00"}')" \
  | jq '{amount, currency, to_amount, to_currency, exchange_rate, rate_source, rate_date}'
```

The response (and every later GET/list) also has `conversion_rates`, both
directions computed from the stored amounts, e.g.
`[{"from":"USD","to":"THB","rate":"33.615"},{"from":"THB","to":"USD","rate":"0.02974862413"}]`;
use it to tell the user "1 USD = 33.615 THB (1 THB = 0.0297 USD)". It is
read-only (`null` for same-currency transfers); never send it back.

Errors: **422** `exchange_rate_unsupported` (pair not covered by ECB, e.g.
VND) → ask the user for the received amount and resend with `to_amount`;
**503** / **502** `exchange_rate_unavailable` → retry later or ask for
`to_amount`. Nothing is saved on failure. Preview a rate without writing:
`GET /api/exchange-rates?from=USD&to=THB&date=2026-10-12` (see reference.md).

### Bulk import (bank statement / PDF list)

Use `POST /api/transactions/bulk` to record many parsed rows at once (max 500
per request). Items have the single-create shape. Give every row a stable
`import_ref`: the bank reference, or a hash of date+amount+description(+seq).
A re-sent row is then `skipped` and never duplicated. Always send a dry run
first, show the user the summary, then send the same body for real (atomic by
default: on 422/503 nothing is written).

```bash
BODY=$(jq -n --arg b "$WALLET_UID" '{dry_run:true, transactions:[
  {type:"expense", amount:"45.00", currency:"THB", balance_uid:$b, spent_at:"2026-10-01T08:15:00+07:00", note:"COFFEE", import_ref:"stmt-2026-10:0001"},
  {type:"income",  amount:"120.00", currency:"THB", balance_uid:$b, spent_at:"2026-10-02T10:00:00+07:00", note:"REFUND", import_ref:"stmt-2026-10:0002"}]}')
curl -sS -b "$JAR" -c "$JAR" -X POST "$BASE/api/transactions/bulk" -H 'Content-Type: application/json' -d "$BODY" \
  | jq '{created, skipped, failed, valid, errors: [.results[] | select(.status=="failed") | {index, error}], balances: [.balances[] | {name, balance, debt}]}'
# looks right → same body with dry_run:false (expect 201; 200 if everything was already imported)
curl -sS -b "$JAR" -c "$JAR" -X POST "$BASE/api/transactions/bulk" -H 'Content-Type: application/json' \
  -d "$(echo "$BODY" | jq '.dry_run=false')" | jq '{created, skipped, failed}'
```

Amounts are always positive: purchases and fees are `expense`; refunds and
credits to a bank account are `income` (a refund on a credit card can't be
recorded yet, so list those rows for the user); card payments and moves between own accounts are
`transfer` (import them from one side only). Never `balance_adjustment`. Full
guide: [docs/bulk-import.md](../../docs/bulk-import.md).

## Step 3: list and filter

```bash
# Newest first; from/to are inclusive RFC 3339 instants (encode + as %2B)
curl -sS -b "$JAR" -c "$JAR" \
  "$BASE/api/transactions?from=2026-10-01T00:00:00%2B08:00&to=2026-10-31T23:59:59%2B08:00&type=expense&limit=100" \
  | jq -r '.count, (.transactions[] | [.spent_at, .type, .amount, .currency, .account, .note] | @tsv)'
```

Other filters: `category_uid=<uid>`, `type=income|transfer|asset_purchase|balance_adjustment`.
`limit` is 1–5000 (default **500**; if `count` equals the limit there may be
more — narrow the date range). There is no server-side sum: total the
`amount` strings per currency yourself and exclude transfers, asset purchases
and balance adjustments from spending/income totals. Category names are not
on transactions; join `category_uid` with `GET /api/categories`.

## Step 4: edit (PUT = full replace + version)

Balances and transactions have a `version` (also the `ETag` header). PUT must
send the **whole record** (omitted optional fields such as `note` or
`category_uid` are cleared) plus `If-Match: "<version>"`. Workflow: GET →
copy only the editable fields → change → PUT.

```bash
TX=$(curl -sS -b "$JAR" -c "$JAR" "$BASE/api/transactions/$TX_UID")
echo "$TX" \
  | jq '{amount, currency, type, balance_uid, to_balance_uid, category_uid, spent_at, note}
        + (if .rate_source == "manual" then {to_amount} else {} end)
        | with_entries(select(.value != null))
        | .note = "Lunch with team"' \
  | curl -sS -b "$JAR" -c "$JAR" -X PUT "$BASE/api/transactions/$TX_UID" \
      -H 'Content-Type: application/json' -H "If-Match: \"$(echo "$TX" | jq .version)\"" \
      --data-binary @- \
  | jq '{note, version, updated_at}'
```

- **428** `version_required` → you forgot `If-Match`; add it.
- **409** `version_conflict` → someone (or a transaction moving the balance)
  changed it; the body has `current`. Re-read, re-apply **the same change**,
  PUT with the new version. Don't blindly overwrite with your old values.
- Balance versions go up whenever a transaction moves the balance, so always
  re-GET a balance right before editing it.
- Editable fields — balance: `name, type (unchanged), currency, description,
  balance` or `debt, limit`; category: `name, type, description` (categories
  are not versioned, no `If-Match`).

**Editing a balance amount** (`balance` / `debt`) is a manual override: it
sets the number and the server records a read-only `balance_adjustment`
transaction for the difference. Only do it when the user explicitly wants to
correct the balance; to record spending, create a transaction instead.

## Step 5: delete

```bash
TX_VERSION=$(curl -sS -b "$JAR" -c "$JAR" "$BASE/api/transactions/$TX_UID" | jq .version)
curl -sS -b "$JAR" -c "$JAR" -o /dev/null -w '%{http_code}\n' -X DELETE \
  "$BASE/api/transactions/$TX_UID" -H "If-Match: \"$TX_VERSION\""   # 204
```

Deleting a transaction reverses its balance effect. `If-Match` is optional
on DELETE but recommended (409 if the record changed). Confirm with the user
first. A category used by any transaction cannot be deleted (409).

## Errors

All errors are JSON `{"error": "...", "fields": {...}, "code": "..."}`.

| status | meaning | what to do |
|---|---|---|
| 400 | malformed JSON, unknown field, bad query param, uid not UUID-shaped, bad `If-Match` | fix the request (send only documented fields); don't retry as-is |
| 401 | not logged in / session expired (`unauthenticated`), bad login (`invalid_credentials`), no account yet (`setup_required`) | log in once and retry; on bad credentials stop and tell the user |
| 403 | `csrf` (Origin / Sec-Fetch-Site), `registration_closed`, wrong current password | drop `Origin`/`Sec-Fetch-Site`; don't retry others |
| 404 | uid not found (or another user's category), unknown path/method | re-list to get the right uid |
| 409 | `version_conflict` (re-read, re-apply, retry), `balance_adjustment_readonly`, `balance_in_use` (currency change), duplicate name, category in use | handle per code; tell the user for name/in-use conflicts |
| 415 | body not `application/json` | add `Content-Type: application/json` |
| 422 | validation failed; `fields` says which field and why (e.g. currency ≠ balance currency, wrong balance type, category type mismatch, too many decimals) | fix the named fields; ask the user if the fix needs information you don't have |
| 428 | `version_required` | resend with `If-Match: "<version>"` |
| 429 | login throttled (`throttled`) | wait `Retry-After` seconds; don't loop |
| 502 / 503 | exchange-rate service failed (`exchange_rate_unavailable`) | retry later, or resend with `to_amount` from the user |

## More

- [reference.md](reference.md): every endpoint, field and query parameter;
  balances and categories CRUD; currencies and exchange-rate endpoints;
  response shapes; the helper script.
- `GET $BASE/api/openapi.yaml` (or `.json`) for machine-readable discovery;
  Swagger UI at `$BASE/swagger/`.
