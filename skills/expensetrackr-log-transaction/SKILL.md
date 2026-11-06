---
name: expensetrackr-log-transaction
description: Record a single expense, income, transfer, asset purchase, or return on investment (dividend, interest) in expensetrackr with a personal API token. Use when the user asks to log, record, or add an expense, income, transfer, asset purchase, or investment return in expensetrackr.
---

# Log a transaction (expensetrackr)

Focused skill for **creating one transaction** via `POST /api/transactions`.
For lists, edits, deletes, bulk import, or balances CRUD, use
[expensetrackr-api](../expensetrackr-api/SKILL.md). Authoritative field rules
live in `GET $BASE/api/openapi.yaml` and the Go types in `pkg/transaction`.

## Safety rules (read first)

- **Never invent amounts, accounts, categories, or dates.** If the user did
  not say which account paid, what the amount was, or when it happened, ask.
  Don't guess a time zone: use the user's offset (ask if unknown).
- **Ask when the account (or category) is ambiguous** — zero matches or more
  than one. Don't create balances or categories unasked.
- **Talk in names, not uids.** Use uids only in API calls; confirm back with
  account/category names, amounts and dates.
- **Never create `balance_adjustment`.** The server records those itself when
  a balance amount is edited; POST with that type is **422**.
- **Protect the token.** Read it from `EXPENSETRACKR_TOKEN`. Never echo, log,
  or put it in argv (process lists show curl `-H "Authorization: Bearer …"`).
  Pass the header through a curl config on stdin (`-K -`) or `-H @file`.

Generic example names in this skill (Wallet, Credit Card, USD Account, Laptop,
Food) are placeholders — substitute the user's real names.

## Setup

```bash
BASE="${EXPENSETRACKR_URL:-http://127.0.0.1:8080}"   # no trailing slash
# EXPENSETRACKR_TOKEN must be a write-scoped personal API token (ext_…)
# created in Settings → API tokens (or POST /api/tokens with a session).
```

Needs `curl` and `jq`. Optional helper:
[scripts/log-tx.sh](scripts/log-tx.sh) resolves names, POSTs, and prints a
summary.

### Auth header without leaking the token

curl accepts a header from a file (`-H @path`) or a config on stdin (`-K -`).
Both keep the secret out of argv:

```bash
# Preferred for POST (stdin is free for -K only when there is no body on stdin)
auth_hdr() {
  # Writes "Authorization: Bearer …" to a 0600 temp file; prints its path.
  # Caller must rm -f the path.
  local f
  f=$(mktemp) || return 1
  chmod 600 "$f"
  # Token from env only — never pass it as a function/CLI argument.
  printf 'Authorization: Bearer %s\n' "${EXPENSETRACKR_TOKEN:?set EXPENSETRACKR_TOKEN}" >"$f"
  printf '%s\n' "$f"
}

# GET example (config on stdin; fine when curl does not also need stdin for the body)
etr_get() {
  printf 'header = "Authorization: Bearer %s"\n' "${EXPENSETRACKR_TOKEN:?}" \
    | curl -sS -K - -H 'Accept: application/json' "$BASE$1"
}
```

A **session cookie** login (see expensetrackr-api) is only a fallback when no
API token is available. Prefer the token: Bearer requests skip the Origin /
`Sec-Fetch-Site` CSRF checks; bodies must still be `Content-Type:
application/json`. Tokens cannot manage tokens or credentials
(`403 session_required`). A `read` token on POST is **403**
`insufficient_scope`.

## Workflow

### 1. Fetch balances and categories; resolve names → uids

```bash
HDR=$(auth_hdr)
etr_get() { curl -sS -H @"$HDR" -H 'Accept: application/json' "$BASE$1"; }

etr_get /api/balances \
  | jq -r '.balances[] | [.uid, .name, .type, .currency, (.balance // .debt)] | @tsv'
etr_get /api/categories \
  | jq -r '.categories[] | [.uid, .name, .type] | @tsv'

balance_uid() {
  etr_get /api/balances \
    | jq -r --arg n "$1" '
        [.balances[] | select((.name | ascii_downcase) == ($n | ascii_downcase))]
        | if length == 1 then .[0].uid else empty end'
}
category_uid() {
  # $1 = name, $2 = expense|income (filters the list)
  etr_get "/api/categories?type=$2" \
    | jq -r --arg n "$1" '
        [.categories[] | select((.name | ascii_downcase) == ($n | ascii_downcase))]
        | if length == 1 then .[0].uid else empty end'
}
balance_currency() {
  etr_get /api/balances \
    | jq -r --arg n "$1" '
        .balances[] | select((.name | ascii_downcase) == ($n | ascii_downcase)) | .currency' \
    | head -n1
}

WALLET_UID=$(balance_uid Wallet)
CARD_UID=$(balance_uid "Credit Card")
USD_UID=$(balance_uid "USD Account")
LAPTOP_UID=$(balance_uid Laptop)
FOOD_UID=$(category_uid Food expense)
# empty → ask the user; never invent a uid
```

Balance types: `payment_account`, `other_asset` (assets, field `balance`);
`credit_card`, `other_liability` (liabilities, fields `debt`, `limit`,
`available`). Names are unique per user (case-insensitive).

### 2. Pick the transaction type

| type | when | source `balance_uid` | `to_balance_uid` | category |
|---|---|---|---|---|
| `expense` (default if omitted) | spending | `payment_account` or `credit_card` | — | optional, must be `expense` |
| `income` | money in / cash rebate | `payment_account`, `other_asset` or `credit_card` | — | optional, must be `income` |
| `transfer` | move between own balances (incl. paying a card) | any | any other balance | **not allowed** |
| `asset_purchase` | buy a tracked asset | `payment_account` or `credit_card` | an `other_asset` | optional, `expense` category |
| `return_on_investment` | dividend / interest from a tracked investment (works like income) | `payment_account` or `credit_card` (received into) | **required**: the `other_asset` it came from (value **not** changed) | optional, `income` category |
| `balance_adjustment` | **never** send this | — | — | — |

### 3. Build the body (exact API rules)

| field | rule |
|---|---|
| `amount` | decimal **string**, > 0, at most the currency's minor decimals (`"120.50"` THB, `"1500"` JPY). Never negative. |
| `currency` | ISO 4217; **must equal the source balance's currency** (422 otherwise). |
| `balance_uid` | UUID of the source / paying / receiving balance. |
| `to_balance_uid` | required for `transfer`, `asset_purchase` and `return_on_investment`; must differ from `balance_uid`. |
| `category_uid` | optional UUID; forbidden on transfers; type must match (see table). |
| `spent_at` | RFC 3339 **with offset**: `2026-10-06T12:30:00+08:00` (no date-only, no naive local time). |
| `note` | optional, ≤ 1000 characters. |
| `to_amount` | **transfer only.** Same-currency: omit, or equal `amount`. Cross-currency: omit → server uses Frankfurter (ECB) for the `spent_at` date; or send the amount received in the destination currency (`rate_source: "manual"`). Never invent a received amount. |
| `to_currency` | optional on transfers; if sent must equal the destination balance's currency. |
| `import_ref` | optional idempotency key (≤ 200 chars); usually omit for a one-off log. |

Unknown JSON fields → **400**. Bodies must be `Content-Type: application/json`
(**415** otherwise — plain `curl -d` without `-H` sends form-urlencoded).

### 4. POST /api/transactions

```bash
post_tx() {
  # stdin: JSON body; prints response body; exit 1 on non-2xx
  local body status out hdr
  body=$(cat)
  hdr=$(auth_hdr)
  out=$(mktemp); trap 'rm -f "$out" "$hdr"' RETURN
  status=$(curl -sS -o "$out" -w '%{http_code}' -X POST "$BASE/api/transactions" \
    -H @"$hdr" -H 'Content-Type: application/json' -H 'Accept: application/json' \
    --data-binary "$body")
  cat "$out"
  echo >&2
  [ "$status" = 201 ] || { echo "HTTP $status" >&2; return 1; }
}
```

Success is **201** with the record (`uid`, `version`, `ETag`, denormalized
`account` / `to_account`, and for cross-currency: `to_amount`, `exchange_rate`,
`rate_source`, `rate_date`, `conversion_rates`). Balances move in the same DB
transaction.

### 5. Confirm to the user in names

Re-fetch the affected balance(s) and speak in names, e.g. "Logged 120.50 THB
lunch (Food) from Wallet on 6 Oct; Wallet is now 1,879.50 THB." For a
cross-currency transfer, include both sides and the rate from
`conversion_rates` (e.g. "1 USD = 33.615 THB").

```bash
etr_get /api/balances \
  | jq -r --arg n Wallet '
      .balances[] | select((.name | ascii_downcase) == ($n | ascii_downcase))
      | "\(.name): \(.balance // .debt) \(.currency)"'
```

### 6. Errors

All errors are JSON `{"error":"…","fields":{…},"code":"…"}` (`fields` /
`code` optional).

| status | typical code / cause | what to do |
|---|---|---|
| **400** | malformed JSON, unknown field, bad uid shape | fix the request; don't retry as-is |
| **401** | `unauthenticated` (no/bad credentials) or `invalid_token` | check `EXPENSETRACKR_TOKEN`; stop and tell the user — don't loop |
| **403** | `insufficient_scope` (read token on POST), `session_required` (token on a session-only path), or `csrf` (cookie client sent a bad Origin) | for a read token: ask the user for a **write** token; don't retry the same token |
| **404** | uid not found / another user's object | re-list balances/categories; ask if the name is wrong |
| **409** | e.g. `duplicate_import_ref` | tell the user; don't invent a new ref unless they want a distinct row |
| **415** | body not `application/json` | add `Content-Type: application/json` |
| **422** | validation (`fields` names the problem); `exchange_rate_unsupported` | fix the named fields; for unsupported FX pairs ask for `to_amount` and resend |
| **502 / 503** | `exchange_rate_unavailable` | retry later, or resend with user-supplied `to_amount` |

## Copy-paste examples

Set `BASE`, `EXPENSETRACKR_TOKEN`, and the `*_UID` variables from step 1 first.
Every example uses `-H @file` so the token never appears in argv.

```bash
HDR=$(auth_hdr)
cleanup() { rm -f "$HDR"; }; trap cleanup EXIT

# Expense: 120.50 THB lunch from Wallet
curl -sS -H @"$HDR" -H 'Content-Type: application/json' -X POST "$BASE/api/transactions" \
  --data-binary "$(jq -n --arg b "$WALLET_UID" --arg c "$FOOD_UID" \
    '{type:"expense", amount:"120.50", currency:"THB", balance_uid:$b, category_uid:$c,
      spent_at:"2026-10-06T12:30:00+08:00", note:"Lunch"}')" \
  | jq '{uid, type, amount, currency, account, note, spent_at, version}'

# Income into Wallet
curl -sS -H @"$HDR" -H 'Content-Type: application/json' -X POST "$BASE/api/transactions" \
  --data-binary "$(jq -n --arg b "$WALLET_UID" \
    '{type:"income", amount:"50000", currency:"THB", balance_uid:$b,
      spent_at:"2026-10-01T09:00:00+08:00", note:"Salary"}')" \
  | jq '{uid, type, amount, account, note}'

# Same-currency transfer: pay 5000 THB of the credit card from Wallet
curl -sS -H @"$HDR" -H 'Content-Type: application/json' -X POST "$BASE/api/transactions" \
  --data-binary "$(jq -n --arg b "$WALLET_UID" --arg t "$CARD_UID" \
    '{type:"transfer", amount:"5000", currency:"THB", balance_uid:$b, to_balance_uid:$t,
      spent_at:"2026-10-07T22:00:00+08:00", note:"Card payment"}')" \
  | jq '{uid, type, amount, account, to_account}'

# Cross-currency transfer: 100 USD Account → Wallet (omit to_amount → Frankfurter)
# Prefer sending to_amount when the user knows the received amount.
curl -sS -H @"$HDR" -H 'Content-Type: application/json' -X POST "$BASE/api/transactions" \
  --data-binary "$(jq -n --arg b "$USD_UID" --arg t "$WALLET_UID" \
    '{type:"transfer", amount:"100", currency:"USD", balance_uid:$b, to_balance_uid:$t,
      spent_at:"2026-10-12T10:00:00+08:00", note:"USD to THB"}')" \
  | jq '{uid, amount, currency, to_amount, to_currency, exchange_rate, rate_source, rate_date, conversion_rates}'
```

Asset purchase (optional; same auth pattern):

```bash
curl -sS -H @"$HDR" -H 'Content-Type: application/json' -X POST "$BASE/api/transactions" \
  --data-binary "$(jq -n --arg b "$CARD_UID" --arg t "$LAPTOP_UID" \
    '{type:"asset_purchase", amount:"35000", currency:"THB", balance_uid:$b, to_balance_uid:$t,
      spent_at:"2026-10-08T15:00:00+08:00", note:"Laptop"}')" \
  | jq '{uid, type, amount, account, to_account}'
```

Return on investment (dividend from Gold paid into Wallet; Gold's value does
not change, `to_amount` / `to_currency` are not allowed):

```bash
curl -sS -H @"$HDR" -H 'Content-Type: application/json' -X POST "$BASE/api/transactions" \
  --data-binary "$(jq -n --arg b "$WALLET_UID" --arg t "$GOLD_UID" \
    '{type:"return_on_investment", amount:"250", currency:"THB", balance_uid:$b, to_balance_uid:$t,
      spent_at:"2026-10-09T10:00:00+08:00", note:"Dividend"}')" \
  | jq '{uid, type, amount, account, to_account}'
```

## Helper script

`scripts/log-tx.sh` reads `EXPENSETRACKR_TOKEN` / `EXPENSETRACKR_URL`, resolves
balance and category **names** to uids, POSTs, and prints a short summary:

```bash
scripts/log-tx.sh expense  Wallet 120.50 THB --category Food --at 2026-10-06T12:30:00+08:00 --note Lunch
scripts/log-tx.sh income   Wallet 50000 THB --at 2026-10-01T09:00:00+08:00 --note Salary
scripts/log-tx.sh transfer Wallet "Credit Card" 5000 THB --at 2026-10-07T22:00:00+08:00
scripts/log-tx.sh transfer "USD Account" Wallet 100 USD --to-amount 3361.50 --at 2026-10-12T10:00:00+08:00
scripts/log-tx.sh return_on_investment Wallet Gold 250 THB --category Dividends --at 2026-10-09T10:00:00+08:00
```

## More

- Broad API skill: [../expensetrackr-api/SKILL.md](../expensetrackr-api/SKILL.md)
- Auth / tokens: [../../docs/auth.md](../../docs/auth.md)
- Live OpenAPI: `GET $BASE/api/openapi.yaml`
