# Balances: automatic adjustment, manual edits and versioning (schema v11–v12)

Balances now follow the transactions recorded against them. You can still set
the amount by hand when it has to match a statement. Every balance and
transaction has a **version**, so two edits based on the same data cannot
silently overwrite each other.

## 1. Automatic adjustment from transactions

Each transaction moves the balances it names. The transaction row and the
balance changes are written **in the same database transaction**: either
both are saved or neither is.

Think of a balance's **value**:
- For an asset (`payment_account`, `other_asset`), the value is its `balance`.
- For a liability (`credit_card`, `other_liability`), the value is minus its
  `debt`.

The source of a transaction loses value and the receiver gains it. `amount`
is always > 0.

| Transaction | Balance | Asset (`balance`) | Liability (`debt`) | Example |
|---|---|---|---|---|
| `expense` | `balance_uid` (payment_account / credit_card) | `balance −= amount` | `debt += amount` | lunch paid by debit card / credit card |
| `income` | `balance_uid` (payment_account / other_asset) | `balance += amount` | n/a (not allowed) | salary into a bank account |
| `transfer` (source) | `balance_uid` (any type) | `balance −= amount` | `debt += amount` | moving savings out; drawing a loan; cash advance |
| `transfer` (destination) | `to_balance_uid` (any type) | `balance += amount` | `debt −= amount` | topping up an account; paying a card; repaying a loan |

The code is `transaction.Effects` (signs) and
`store.applyBalanceEffects` (writes).

### Edits and deletes

- **Update:** the old effect is reversed and the new one applied, in one
  database transaction. This covers changes of amount, type (expense ↔ income
  ↔ transfer), source account and transfer destination.
  - Changes are summed per balance. A balance whose net change is zero (for
    example a note-only edit) is not written, and its version does not change.
- **Delete:** the effect is reversed.
- **Balance deleted meanwhile:** if a referenced balance was deleted, its part
  of the reversal is skipped. The other balance is still adjusted.
- **Older transactions:** transactions recorded **before schema v11** never
  moved a balance (`"adjusts_balances": false` in the API). Editing or
  deleting them leaves balances alone. Correct those by hand if needed (see
  §2). Every transaction created from v11 on has `"adjusts_balances": true`.

### Currency must match (no FX yet)

A transaction's `currency` must equal the currency of every balance it moves
(`balance_uid`, and `to_balance_uid` for transfers). Otherwise the request is
rejected with **422** and nothing is written:

```json
{"error":"validation failed","fields":{"balance_uid":"balance \"KBank\" is in THB but the transaction is in USD; the currencies must match (no exchange-rate conversion yet)"}}
```

- The error is on `to_balance_uid` when the destination is the mismatch.
- So a transfer between a THB and an SGD account cannot be recorded until FX
  support exists. Adjust both balances by hand instead.
- A balance's **currency cannot change while any transaction references it**:
  **409** with `"code":"balance_in_use"`. Those transactions' amounts are in
  the old currency and would be reversed against it. Unreferenced balances
  may still change currency.
- The web form fills in the balance's currency when you pick a balance.

### Negative debt and over-limit

- **Negative debt is allowed.** A card or loan that is overpaid is in credit,
  e.g. `debt: "-50.00"`.
- Schema v11 drops the old `CHECK (debt_minor >= 0)`, and manual edits may set
  a negative debt too. `limit` must still be ≥ 0.
- `over_limit` (debt > limit) and `available` (limit − debt) are still
  computed on every read. An overpaid card's available credit is above its
  limit.
- A change that would overflow the stored integer is rejected (422 on
  `amount`). With 13 integer digits per amount this is theoretical.

## 2. Manual adjustment (PUT/PATCH on a balance)

You can still set `balance`, `debt` or `limit` directly with the existing
`PUT`/`PATCH /api/balances/:uid` (versioned like every other write, see
below). **A manual value overrides** whatever transactions did. Later
transactions then move the balance from the new value.

There is no separate adjustments API or audit table: the balance's `version`
and `updated_at` change, and the transactions remain the record of automatic
changes. The web edit page shows a short hint that typed amounts override the
automatic ones.

## 3. Optimistic locking (versions)

Each balance and transaction has `version INTEGER NOT NULL DEFAULT 1`. It
starts at 1 and goes up by one on **every** change:
- a PUT or PATCH;
- for a balance, also every transaction create, update or delete that moves
  it.

Exposed as:
- `"version": n` in every JSON representation, lists included;
- an **`ETag: "n"`** header on POST (201), GET, PUT and PATCH of a single
  balance or transaction. This is a strong ETag: the representation of a
  given version never changes.

### Requests

PUT and PATCH on `/api/balances/:uid` and `/api/transactions/:uid` **require**
the version the edit is based on, in either of two places:

1. **`If-Match: "n"`** header. This is preferred; `W/"n"` is accepted too.
2. **`"version": n`** in the JSON body. This is handy for clients that cannot
   set headers, or that send back a GET response.

If both are sent they must agree (otherwise 400).

| Situation | Response |
|---|---|
| neither sent | **428 Precondition Required**, `"code":"version_required"`, nothing changed |
| malformed `If-Match` (not one `"n"` tag; `*` and lists are not supported), `version` not a positive integer, or the two disagree | **400** |
| version is stale (the record changed since it was read) | **409 Conflict**, `"code":"version_conflict"`, `"current": {…the record now…}`, `ETag` of the current version; nothing changed |
| version matches | 200, the updated record with `version` + 1 and its new `ETag` |

Checks run in this order:
1. 400/404 for the uid;
2. 400 for the body;
3. 428/400 for the precondition;
4. **409 before 422**: a stale client must reload before its input is judged.

The version is checked again inside the write transaction
(`UPDATE … WHERE id = ? AND version = ?`). So of several concurrent writers
holding the same version, exactly one wins and the rest get 409. The tests
cover this.

**DELETE** takes an optional `If-Match`. If one is sent and is stale, the
answer is 409 and nothing is deleted.

Typical flow:

```sh
curl -si http://127.0.0.1:8080/api/balances/$UID | grep -i etag     # ETag: "4"
curl -s -X PATCH http://127.0.0.1:8080/api/balances/$UID \
  -H 'Content-Type: application/json' -H 'If-Match: "4"' \
  -d '{"balance":"1200.50"}'                                         # 200, version 5
# Same request again (still If-Match "4"):
# 409 {"error":"version conflict: …","code":"version_conflict","fields":{"version":"is stale; the current version is 5"},"current":{…}}
```

**Why require it?**
- **Balances:** a balance now changes behind the user's back whenever a
  transaction is saved. Without a precondition, an edit page opened before an
  expense was logged would silently undo that expense.
- **Transactions:** versioned the same way for consistency, since two tabs
  editing one transaction is the same problem.
- **Categories and users:** not versioned yet.

### Web UI

The edit pages send `If-Match` with the version they loaded, and the list
pages send it with Delete. On a 409:
- **Edit page:** shows *"…was changed after you opened this page (for example
  by a transaction or in another tab), so your changes were not saved. It now
  has …"* and a **Reload latest** button. The button reloads the record and
  discards the unsaved edits.
- **List page:** an alert explains that nothing was deleted, and the list is
  refreshed.

## 4. Migrations v11 and v12

Each migration runs in one SQL transaction and is idempotent.

**v11:**

1. **`balances` is rebuilt.** Rows, ids, uids and amounts are copied
   unchanged, and the AUTOINCREMENT sequence is kept. The rebuild adds
   `version` (existing rows: 1) and drops `CHECK (debt_minor >= 0)`, which
   SQLite cannot drop in place. Nothing references `balances` by foreign key,
   so this is safe with `foreign_keys` on. It is skipped if the table already
   has `version` and no debt check.
2. **`transactions` gets new columns:** `version` (existing rows 1) and
   `balance_applied` (existing rows 0, i.e. "never moved a balance"; shown as
   `adjusts_balances`), plus an index on `to_balance_uid`.

**v12:** drops `balance_adjustments` (and its index and AUTOINCREMENT
counter) if present. Only a pre-release build of v11 created that table; on
any other database v12 is a no-op.

**Existing balances are not recomputed** from existing transactions. Their
amounts stay exactly as entered.

## Follow-ups

- FX: allow cross-currency transactions and transfers with a rate.
- Optional "recompute from transactions" tool, or a reconciliation report.
- Versioning for categories and users.
