# Open questions

[← Back to the README](../README.md) · [All docs](README.md)

Ideas and decisions that are still open.

- Authentication: username/password login exists (docs/auth.md). Transactions,
  balances and categories are per user, and each user manages only their own
  account. Follow-ups: SSO; roles (admin accounts); password reset.
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
  `EXPENSE_*` env vars and the `expense` database name. Does
  "transactions" mean income/transfers will be recorded too, which would need
  a type/sign field?
- Refunds or negative amounts (currently rejected; amounts must be > 0).
