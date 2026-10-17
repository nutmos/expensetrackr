# Bulk import (`POST /api/transactions/bulk`)

Creates up to **500** transactions in one request. The main use is an AI agent
that has parsed a bank statement, card statement or PDF transaction list and
wants to record every row at once, safely and repeatably.

Every item has exactly the shape of `POST /api/transactions` and goes through
the same code: `CreateInput.Validate`, balance/category resolution
(`attachPaymentBalance`), cross-currency conversion and the store's insert +
balance adjustment. A bulk import has the same result as the same single
creates sent one by one in the same order.

## Request

```json
{
  "transactions": [
    {"type": "expense", "amount": "45.00", "currency": "THB",
     "balance_uid": "4dc73a4a-…", "category_uid": "…",
     "spent_at": "2026-10-01T08:15:00+07:00", "note": "COFFEE SHOP",
     "import_ref": "kbank-2026-10:3f9c…"}
  ],
  "atomic": true,
  "dry_run": false
}
```

| field | default | meaning |
|---|---|---|
| `transactions` | required | 1–500 items, same fields as single POST plus optional `import_ref`. `uid` and `version` are not accepted in items. Inserted in this order. |
| `atomic` | `true` | All or nothing. `false` creates every valid item and reports the others. |
| `dry_run` | `false` | Run everything (validation, balance currency checks, FX conversion, duplicate detection, balance effects), then roll back. |

Limits: more than 500 items is **413** (`code` `too_many_items`); the body may
be at most **2 MiB** (400 "request body too large"); an empty or missing list,
malformed JSON or an unknown top-level field is **400**. A problem *inside one
item* (unknown field, wrong JSON type, `null` item, invalid value) only fails
that item (`code` `invalid_item` or `validation_failed`).

## Response

```json
{
  "created": 2, "skipped": 1, "failed": 0, "valid": 0,
  "dry_run": false, "atomic": true, "written": true,
  "results": [
    {"index": 0, "status": "created", "transaction": {"uid": "…", "import_ref": "…", "...": "..."}},
    {"index": 1, "status": "skipped", "existing_uid": "ea67cb7c-…"},
    {"index": 2, "status": "created", "transaction": {"...": "..."}}
  ],
  "balances": [{"uid": "…", "name": "KBank", "balance": "10699.50", "version": 7, "...": "..."}]
}
```

- `results` has one entry per input item, in input order (`index` = array
  position).
- `status`:
  - `created`: stored. `transaction` is the full record, as from single POST.
  - `skipped`: duplicate `import_ref` (see below); `existing_uid` is the
    transaction that already has it. Not an error.
  - `failed`: `error` is `{"message", "code", "fields"}`. `code` is one of
    `validation_failed`, `invalid_item`, `exchange_rate_unsupported`,
    `exchange_rate_unavailable` or `balance_adjustment_readonly`. `fields`
    uses the same per-field messages as single POST.
  - `valid`: would be created but nothing was written. This happens in a dry
    run, or in an atomic batch that was rolled back because another item
    failed. `transaction` shows the computed record (`to_amount`, rate,
    `conversion_rates`, account names), with an empty `uid` and `created_at` and `version` 0.
- `written`: whether the database transaction was committed.
- `balances`: every balance touched by created or valid items, as it is after
  the request. For a dry run or a rolled-back batch, this is how it *would*
  be. Use it to check a dry run against the statement's closing balance.

Status codes:

| situation | code |
|---|---|
| atomic, committed, ≥ 1 created | **201** |
| atomic, every item skipped | 200 |
| `atomic: false` (whatever the per-item outcome) | 200, check `failed` |
| dry run with no failure, or with `atomic: false` | 200 |
| atomic (incl. dry run) with ≥ 1 failed item | **422**, nothing written, per-item `results` |
| …and a failure was Frankfurter being unreachable / answering badly | **503** / **502** (same body) |
| no session / CSRF / wrong Content-Type | 401 / 403 / 415 |

## How it runs

1. **Validate phase (no DB writes).** Each item is decoded strictly and
   validated, and its balances and category are resolved. A cross-currency
   transfer without `to_amount` gets its Frankfurter rate here: from the
   `fx_rates` cache, or else fetched and cached. Within one request each
   (pair, date) is looked up at most once, failures included, so an outage
   costs one attempt and not 500. **No network call happens inside the
   database transaction.**
2. **Write phase.** One SQL transaction for the whole batch. Each item runs
   inside a `SAVEPOINT`: duplicate check → insert → balance adjustment. If
   the store rejects the item (for example a currency mismatch with the
   balance), only that savepoint is rolled back. Balances move item by item
   in input order, and each balance's `version` goes up once per item that
   touches it, just as with single creates.
3. **Finish.** Dry run, or atomic with any failure: roll back. Otherwise
   commit.

The server keeps one SQLite connection, so a batch is serialised with other
writes. 500 items take about 0.1 s on a laptop (`TestBulk500Performance`
requires under 3 s).

## Idempotency: `import_ref`

`import_ref` is an optional string of at most 200 characters (trimmed, no
control characters), stored on the transaction (schema v18) and returned in
every transaction JSON. It is **unique per `balance_uid`**: a partial unique
index on `(balance_uid, import_ref) WHERE import_ref IS NOT NULL`.

- Bulk: if an item's `(balance_uid, import_ref)` already exists, in the
  database or earlier in the same request, the item is `skipped` with
  `existing_uid` and nothing changes. Re-sending a whole statement is safe,
  and only new rows are added.
- The same ref on a **different** balance is a different key. Two statements
  can both have a row "0001".
- Single `POST /api/transactions` accepts `import_ref` too. A duplicate there
  is **409** `code` `duplicate_import_ref`, not a skip.
- `PUT /api/transactions/{uid}`: if `import_ref` is omitted or `null`, the
  stored ref is kept. A string replaces it (same uniqueness rule, 409 on
  conflict) and `""` clears it. The web UI never sends it, so edits there
  keep the ref.
- Items without `import_ref` are never deduplicated.

## Guidance for agents importing statements

1. **Pick the balance first.** Look up the balance the statement belongs to
   (`GET /api/balances`) and use its uid as `balance_uid` for every row.
   `currency` must equal that balance's currency. Rows in a foreign currency
   on a card statement are normally already converted by the bank, so use the
   billed amount in the card's currency and put the original amount in
   `note`.
2. **Make a stable `import_ref` for every row.** Use the bank's own
   reference or transaction id if the statement has one, e.g.
   `"kbank:FT2610030001234"`. If not, use a hash of the raw row, e.g.
   `sha256(posting_date|amount|description|running_balance)` hex. Include a
   per-statement sequence number when two rows on the same day are otherwise
   identical, such as two identical coffees. Build the ref only from data
   that will be the same the next time the same statement is parsed, never
   from the parse time.
3. **Signs and types.** `amount` is always **positive**. The type gives the
   direction:
   - money out for a purchase or fee: `type: "expense"`
   - money in (salary, interest) and **refunds, reversals and cashback
     credits** to a bank account: `type: "income"`. Income can only go to a
     `payment_account` or `other_asset`. A **refund or credit on a credit
     card statement** cannot be recorded yet, because negative amounts and
     income to a card are rejected (see "Not supported yet" in the README).
     Leave those rows out, list them for the user, and suggest a manual edit
     of the card's debt, which records a balance adjustment.
   - **credit card payments** and moves between your own accounts:
     `type: "transfer"`, from the paying account to the card or other
     account. On a card statement, the "PAYMENT – THANK YOU" line is the
     destination side of a transfer that may already have been imported from
     the bank statement. Import it once only, from one side.
   - buying something tracked as an `other_asset`: `type: "asset_purchase"`
   - never `balance_adjustment`. It is rejected per item. To reconcile a
     final difference, edit the balance instead.
4. **`spent_at`** must have an offset. Use the bank's local time zone, e.g.
   `2026-10-03T00:00:00+07:00` for a date-only Thai statement.
5. **Categories** are optional and per user. Map them with
   `GET /api/categories` and leave them out when unsure. Expense rows need
   expense categories and income rows need income categories.
6. **Dry run first.** Send the batch with `"dry_run": true`. Fix every
   `failed` item, check that `skipped` only contains rows you expect to have
   imported before, and compare `balances[].balance` / `debt` with the
   statement's closing balance. Show the user a summary.
7. **Then import atomically** with the same body and `"dry_run": false`. If
   it returns 422/503, nothing was written: fix the items and retry. Use
   `"atomic": false` only when the user explicitly wants the good rows now
   and the bad ones reported.
8. **Batch** more than 500 rows into several requests, in date order. Thanks
   to `import_ref`, a retry after a timeout or crash is safe.
9. **Cross-currency transfers** without `to_amount` use the ECB rate for the
   `spent_at` date. Send the bank's actual `to_amount` when the statement
   shows it, so the rate is `manual` and exact.

## FX failures

| Frankfurter outcome | atomic | `atomic: false` |
|---|---|---|
| pair unsupported (e.g. VND) | item `failed` `exchange_rate_unsupported`; 422 | that item fails |
| unreachable / timeout | item `failed` `exchange_rate_unavailable`; **503**, nothing written | that item fails, others are created |
| bad upstream response | as above but **502** | that item fails |

Sending `to_amount` avoids the rate lookup entirely. See
[exchange-rates.md](exchange-rates.md).
