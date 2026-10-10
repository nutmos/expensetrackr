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
- [Balances](balances.md): automatic adjustment from transactions (sign
  rules), manual overrides via PUT recorded as Balance Adjustment
  transactions, optimistic locking (`version`, `ETag`, `If-Match`, 428/409),
  schema v11–v13.
