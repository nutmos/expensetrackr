# Balances: automatic adjustment, manual edits and versioning (schema v11–v13)

Balances now follow the transactions recorded against them. You can still set
the amount by hand when it has to match a statement; that edit is recorded as
a read-only **Balance Adjustment** transaction. Every balance and
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
| `balance_adjustment` | `balance_uid` (any type) | none | none | recorded by a manual balance edit, which already set the value (§2) |

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

You can still set `balance` (assets) or `debt` (liabilities) directly with
the existing `PUT`/`PATCH /api/balances/:uid` (versioned like every other
write, see §3). **A manual value overrides** whatever transactions did; later
transactions move the balance from the new value. There is no separate
adjustments API.

### Balance Adjustment transactions

When a manual edit changes the amount, the server records a transaction of
type **`balance_adjustment`** (shown as **"Balance Adjustment"**) in the
**same database transaction** as the balance update: both are saved or
neither is (a stale or invalid edit records nothing).

| Field | Value |
|---|---|
| `type` | `balance_adjustment` |
| `amount` | the absolute difference, always > 0 (e.g. 1000.00 → 950.00 gives `50.00`) |
| `adjustment_direction` | `increase` or `decrease` of the edited amount: `balance` for an asset, `debt` for a liability (so `decrease` on a card means its debt went down). `null` on every other type |
| `balance_uid`, `account` | the edited balance and its (new) name |
| `currency` | the balance's currency |
| `spent_at` | the time of the edit: the **server's** current local time with its UTC offset (e.g. `2026-10-08T21:30:00+08:00`), whole seconds |
| `note` | generated, e.g. `Manual edit of balance: 1000.00 → 950.00` |
| `category_uid`, `to_balance_uid` | `null` |
| `adjusts_balances` | `false`: it never moves a balance (no double counting) |
| `version` | 1 (it never changes) |

When nothing is recorded:
- **Limit changes** (`limit` is not money moving): a limit-only edit records
  nothing; an edit of debt and limit records only the debt change.
- **No change in the amount**: renames, description edits, or re-sending the
  same value.
- **Currency change in the same edit**: the old and new amounts are in
  different currencies. This is only possible while no transaction references
  the balance; afterwards the currency is fixed (409 `balance_in_use`), and a
  recorded adjustment counts as a reference.

**Read-only via the API.** Adjustments are created only by balance edits:
- `POST /api/transactions` with `"type":"balance_adjustment"`, or a PUT/PATCH
  that changes a transaction to that type: **422** on `type`.
- `PUT`, `PATCH` or `DELETE` on an existing adjustment: **409** with
  `"code":"balance_adjustment_readonly"` (whatever the body or version).
  To correct one, edit the balance again (which records another adjustment).
- They appear in `GET /api/transactions` and can be filtered with
  `?type=balance_adjustment`.

Guarded in the database too: transactions `CHECK`s require
`adjustment_direction` exactly on `balance_adjustment` rows, and forbid
those rows a destination, a category or `balance_applied = 1`.

**Web UI.** The balance edit page notes that a typed balance/debt is recorded
as a Balance Adjustment. On the transactions list, adjustments have their own
style, the label "Balance Adjustment", a signed amount (+/−), "Automatic"
instead of Edit/Delete, and are **excluded from the expense/income totals**
(like transfers). The list has a Type filter. Opening an adjustment's edit URL
shows a read-only message.

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

## 4. Migrations v11–v13

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

**v13:** `transactions` is rebuilt, since SQLite cannot change a `CHECK` in
place: create the new table, copy every row (same ids, uids and values;
`adjustment_direction` NULL), drop the old table, rename, recreate all
indexes (including the unique uid index) and restore the AUTOINCREMENT
counter so ids of deleted rows are not reused. The new table allows type
`balance_adjustment`, adds `adjustment_direction` and the table `CHECK`s
above. Nothing references `transactions` by foreign key. Skipped if already
done. A row violating the current constraints would abort the migration (and
roll it back) rather than be altered.

**Existing balances are not recomputed** from existing transactions. Their
amounts stay exactly as entered.

## Follow-ups

- FX: allow cross-currency transactions and transfers with a rate.
- Optional "recompute from transactions" tool, or a reconciliation report.
- Versioning for categories and users.
