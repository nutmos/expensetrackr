# docs

Project documentation that is not code (design notes, decisions, guides).

The code lives at the repo root (`go.mod`, [`cmd/`](../cmd), [`pkg/`](../pkg));
see the [top-level README](../README.md) for how to build, run and test it
(`make help`).

## API documentation

The OpenAPI 3.0 spec is hand-written and embedded in the binary at
[`pkg/apidoc/openapi.yaml`](../pkg/apidoc/openapi.yaml) (the source of truth;
it has to live in a package directory so `go:embed` can include it).

With the server running (`make run`):

- http://127.0.0.1:8080/swagger/ renders it with Swagger UI (vendored,
  fully offline; `/swagger` redirects there).
- http://127.0.0.1:8080/api/openapi.yaml serves the spec as written, and
  http://127.0.0.1:8080/api/openapi.json serves the same document as JSON.

`pkg/api/openapi_test.go` checks that every Gin `/api` route (method + path)
is an operation in the spec and vice versa, and that the response schemas
match the Go JSON shapes.

## Pages

- [User profiles](user-profile.md): the `users` and `user_identities` tables
  (schema v10), the `/api/users` API, and the auth-readiness decisions.
- [Categories](categories.md): per-user categories stored as one JSON
  row per user (schema v15, issue #34), migration and concurrency.
- [Authentication](auth.md): username/password login, sessions (schema
  v14), first-run setup, public vs protected routes, CSRF and throttling.
- [Exchange rates](exchange-rates.md): cross-currency transfers (schema
  v17), Frankfurter / ECB rates, caching, failure behaviour.
- [Balances](balances.md): automatic adjustment from transactions (sign
  rules), manual overrides via PUT recorded as Balance Adjustment
  transactions, optimistic locking (`version`, `ETag`, `If-Match`, 428/409),
  schema v11–v13.
- [Agent skill](../skills/expensetrackr-api/SKILL.md): `SKILL.md` for LLM
  agents using the HTTP API (auth, CSRF, workflows, errors, safety), with
  [reference.md](../skills/expensetrackr-api/reference.md) and the
  [`etr.sh`](../skills/expensetrackr-api/scripts/etr.sh) curl helper.
