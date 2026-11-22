# Balances: automatic adjustment, manual edits and versioning

Balances now follow the transactions recorded against them. You can still set
the amount by hand when it has to match a statement; that edit is recorded as
a **Balance Adjustment** transaction, which you can later edit or delete like
any other transaction. Every balance and
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
| `income` | `balance_uid` (payment_account / credit_card) | `balance += amount` | `debt −= amount` (may go negative = in credit) | salary into a bank account; cash rebate on a card |
| `transfer` (source) | `balance_uid` (any type) | `balance −= amount` | `debt += amount` | moving savings out; drawing a loan; cash advance |
| `transfer` (destination) | `to_balance_uid` (any type) | `balance += amount` | `debt −= amount` | topping up an account; paying a card; repaying a loan |
| `asset_purchase` (source) | `balance_uid` (payment_account / credit_card) | `balance −= amount` | `debt += amount` | buying a laptop, gold or a car with a debit or credit card |
| `asset_purchase` (destination) | `to_balance_uid` (other_asset only) | `balance += amount` | n/a (not allowed) | the asset bought gains its value |
| `return_on_investment` (receiver) | `balance_uid` (payment_account / credit_card) | `balance += amount` | `debt −= amount` (may go negative = in credit) | a dividend or interest paid into an account or onto a card |
| `return_on_investment` (linked asset) | `to_balance_uid` (other_asset only) | none (link only) | n/a (not allowed) | the investment the return came from keeps its value |
| `asset_sale` (receiver) | `balance_uid` (payment_account / credit_card) | `balance += amount + gain_loss` | `debt −= amount + gain_loss` (may go negative = in credit) | the proceeds of selling gold or shares paid into an account |
| `asset_sale` (sold asset) | `to_balance_uid` (other_asset only) | `balance −= amount` (the principal only); hidden when `entire_asset_sale` | n/a (not allowed) | the asset sold loses the principal's value |
| `repay_debt` (source) | `balance_uid` (payment_account only) | `balance −= amount + interest_amount + fee_amount` | n/a (not allowed) | paying a card bill or a loan instalment from a bank account |
| `repay_debt` (destination) | `to_balance_uid` (credit_card / other_liability) | n/a (not allowed) | `debt −= amount` (the principal only; may go negative = in credit) | the card or loan repaid |
| `balance_adjustment` | `balance_uid` (any type) | `increase`: `balance += amount`; `decrease`: `balance −= amount` | `increase`: `debt += amount`; `decrease`: `debt −= amount` | recorded by a manual balance edit, which already set the value; applied only when the adjustment is later edited or deleted (§2) |

The code is `transaction.Effects` (signs) and
`store.applyBalanceEffects` (writes).

### Edits and deletes

- **Update:** the old effect is reversed and the new one applied, in one
  database transaction. This covers changes of amount, type (expense ↔ income
  ↔ transfer ↔ asset_purchase ↔ return_on_investment ↔ asset_sale ↔ repay_debt), source account and transfer destination.
  - Changes are summed per balance. A balance whose net change is zero (for
    example a note-only edit) is not written, and its version does not change.
- **Delete:** the effect is reversed.
- **Balance missing:** deleting a balance deletes its transactions too (§5),
  but a transaction left from an older version may still reference a deleted
  balance; its part of the reversal is skipped. The other balance is still
  adjusted.
- **`adjusts_balances`:** every transaction has `"adjusts_balances": true`,
  Balance Adjustment rows (§2) included: their change is already in the
  balance (the manual edit set it), so editing or deleting one moves the
  balance like any other transaction. (Until schema v5 adjustments had
  `false` and could not be changed.)

Cross-currency transfers: the source changes by `amount` (source currency)
and the destination by the stored `to_amount` (destination currency). An
edit or delete reverses exactly those stored values — rates are never
refetched for a reversal.

### Currency must match (except cross-currency transfers)

A transaction's `currency` must equal the currency of every balance it moves
(`balance_uid`, and `to_balance_uid` for asset purchases, asset sales and debt repayments; the asset linked by a
return on investment is not moved, so its currency may differ). A **transfer** is
the exception for its destination: if `to_balance_uid` is in another
currency, the destination moves by `to_amount` in its own currency (see
[exchange-rates.md](exchange-rates.md)); the source still moves by
`amount` and must be in `currency`. Otherwise the request is
rejected with **422** and nothing is written:

```json
{"error":"validation failed","fields":{"balance_uid":"balance \"Wallet\" is in THB but the transaction is in USD; the currencies must match"}}
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
- The schema has no `CHECK (debt_minor >= 0)`, and manual edits may set a
  negative debt too. `limit` must still be ≥ 0.
- `over_limit` (debt > limit) and `available` (limit − debt) are still
  computed on every read. An overpaid card's available credit is above its
  limit.
- A change that would overflow the stored integer is rejected (422 on
  `amount`). With 13 integer digits per amount this is theoretical.

## 2. Manual adjustment (PUT on a balance)

You can still set `balance` (assets) or `debt` (liabilities) directly with
the existing `PUT /api/balances/:uid` (a full replace: send the whole
record; versioned like every other
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
| `adjusts_balances` | `true`: its change is already in the balance (recording it does not move the balance again: no double counting) |
| `version` | 1, then +1 on every edit |

When nothing is recorded:
- **Limit changes** (`limit` is not money moving): a limit-only edit records
  nothing; an edit of debt and limit records only the debt change.
- **No change in the amount**: renames, description edits, or re-sending the
  same value.
- **Currency change in the same edit**: the old and new amounts are in
  different currencies. This is only possible while no transaction references
  the balance; afterwards the currency is fixed (409 `balance_in_use`), and a
  recorded adjustment counts as a reference.

**Created only by balance edits; editable and deletable afterwards.**
- `POST /api/transactions` with `"type":"balance_adjustment"`, or a PUT
  that changes another transaction to that type: **422** on `type` (bulk
  import: `balance_adjustment_readonly` per item).
- `PUT /api/transactions/:uid` on an adjustment (versioned as usual) may
  change `amount`, `adjustment_direction` (**required**), `balance_uid` (any
  balance type), `currency` (must match the balance), `spent_at` and `note`.
  `type` must be omitted or `balance_adjustment` (422 otherwise: an
  adjustment stays one); `category_uid`, `to_balance_uid`,
  `to_amount`/`to_currency`, `foreign_amount`/`foreign_currency` and
  `import_ref` are not allowed (422). The old change is reversed and the
  new one applied, like any edit: e.g. turning *decrease 100.00* on an
  account into *decrease 40.00* raises its balance by 60.00; moving an
  *increase* from an account to a card lowers the account and raises the
  card's debt.
- `DELETE` on an adjustment reverts its change: deleting *decrease 50.00*
  (1000.00 → 950.00) puts the balance back up by 50.00. If the balance was
  deleted meanwhile, only the row is removed.
- They appear in `GET /api/transactions` and can be filtered with
  `?type=balance_adjustment`.

Guarded in the database too: transactions `CHECK`s require
`adjustment_direction` exactly on `balance_adjustment` rows, and forbid
those rows a destination or a category.

**Web UI.** The balance edit page notes that a typed balance/debt is recorded
as a Balance Adjustment. On the transactions list, adjustments have their own
style, the label "Balance Adjustment", a signed amount (+/−) and Edit/Delete
like other rows, and are **excluded from the expense/income totals** (like
transfers). The list has a Type filter. The edit form of an adjustment keeps
its type (shown, not changeable) and has a **Direction** field (Increase /
Decrease, with a hint saying whether it moves the balance or the debt); the
adjusted balance may be any balance. Deleting one asks for confirmation and
says that its change will be reverted.

## 3. Optimistic locking (versions)

Each balance and transaction has `version INTEGER NOT NULL DEFAULT 1`. It
starts at 1 and goes up by one on **every** change:
- a PUT;
- for a balance, also every transaction create, update or delete that moves
  it.

Exposed as:
- `"version": n` in every JSON representation, lists included;
- an **`ETag: "n"`** header on POST (201), GET and PUT of a single
  balance or transaction. This is a strong ETag: the representation of a
  given version never changes.

### Requests

PUT on `/api/balances/:uid` and `/api/transactions/:uid` **require**
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
curl -s -X PUT http://127.0.0.1:8080/api/balances/$UID \
  -H 'Content-Type: application/json' -H 'If-Match: "4"' \
  -d '{"name":"Cash","type":"payment_account","currency":"THB","balance":"1200.50"}'                                         # 200, version 5
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

## 4. Schema

These columns are part of the baseline schema (`pkg/store/schema.go`):

- `balances.version` and `transactions.version`: optimistic-locking
  counters, 1 on insert. `balances` has no `CHECK (debt_minor >= 0)`
  (negative debt = in credit); `limit_minor` must be ≥ 0.
- `transactions.balance_applied` (shown as `adjusts_balances`): true when
  the row's change is reflected in its balances. Since v5
  (`pkg/store/migrate_v5.go`) that is every row; before, `balance_adjustment`
  rows had false (the migration sets it to true and drops the check that
  required false).
- `transactions.adjustment_direction` (`increase` / `decrease`): set exactly
  for type `balance_adjustment`; table `CHECK`s keep such rows free of a
  destination or category (`transactions_adjustment_check` since v5).
- Index `idx_transactions_to_balance_uid` on `to_balance_uid`.
- `balances.hidden` (since v7, `pkg/store/migrate_v7.go`): `false` unless the
  balance was hidden (§5).

## 5. Hiding and deleting a balance

Deleting a balance would leave orphaned transactions behind, so there are two
ways to get rid of one (issue #147):

**Hide** (`POST /api/balances/:uid/hide`, undo with `/unhide`): for a closed
account or a paid-off loan whose history you want to keep.
- The balance gets `"hidden": true` and its version goes up by one (hiding a
  hidden balance changes nothing). No body; `If-Match` is optional (409 if
  stale).
- `GET /api/balances` leaves it out, and its `totals` with it, unless
  `?include_hidden=true` (also with `type=` / `payable=`). `GET
  /api/balances/:uid` still returns it.
- Nothing else changes: its amount and transactions stay, the reports still
  count it, it can be edited (a PUT keeps it hidden; `hidden` is not a PUT
  field), and transactions may still use it.
- Web page: **Hide** / **Show** buttons on the list, a **Show hidden** box
  (`/balances?hidden=1`) that lists hidden balances with a *hidden* badge, and
  the transaction form offers a hidden balance only when the transaction being
  edited already uses it (labelled "(hidden)").

**Delete** (`DELETE /api/balances/:uid`): removes the balance **and every
transaction that references it**, as `balance_uid` or `to_balance_uid`
(transfers to or from it, asset purchases into it, returns on investment
linked to it, its balance adjustments).
- Each removed transaction's effect on your **other** balances is reversed,
  exactly as if it had been deleted on its own: deleting a savings account
  that received a 300.00 transfer from Cash puts 300.00 back on Cash; deleting
  an asset bought with a card lowers the card's debt again. Reversals are
  summed per balance, so each touched balance is written once (version +1).
- Everything happens in one database transaction, with the involved balances
  locked in uid order like any transaction write. `If-Match` is optional; a
  stale one is 409 and nothing is deleted.
- Response: **200** `{"deleted_transactions": n}` (it used to be 204 with the
  transactions left behind).
- Web page: **Delete** asks first, saying how many transactions will go, and
  offers **Hide instead**.

## Follow-ups

- FX for other types (e.g. a foreign-currency expense on a card); transfers
  are done ([exchange-rates.md](exchange-rates.md)).
- Optional "recompute from transactions" tool, or a reconciliation report.
- Versioning for categories and users.
