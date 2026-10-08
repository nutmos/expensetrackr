# docs

Project documentation that is not code (design notes, decisions, guides).

The code lives at the repo root (`go.mod`, [`cmd/`](../cmd), [`pkg/`](../pkg));
see the [top-level README](../README.md) for how to build, run and test it
(`make help`).

## Pages

- [User profiles](user-profile.md): the `users` and `user_identities` tables
  (schema v10), the `/api/users` API, and the auth-readiness decisions.
- [Balances](balances.md): automatic adjustment from transactions (sign
  rules), manual overrides via PUT/PATCH recorded as Balance Adjustment
  transactions, optimistic locking (`version`, `ETag`, `If-Match`, 428/409),
  schema v11–v13.
