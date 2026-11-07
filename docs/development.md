# Development

[← Back to the README](../README.md) · [All docs](README.md)

Repository layout, tests and make targets, and a map of the code.

## Repository layout

Modelled on [prometheus-operator](https://github.com/prometheus-operator/prometheus-operator):
`go.mod` at the repo root, binaries under `cmd/`, packages under `pkg/`, a
`Makefile` for common tasks, and documentation in its own folder. Everything
runs from the repo root; no `cd` needed.

```
go.mod, go.sum   module github.com/nutmos/expensetrackr
Makefile         build / run / test / vet / fmt / clean, up / down (Docker)
Dockerfile       server image (multi-stage: Go build -> small Alpine, non-root)
compose.yaml     local stack: Postgres ("db") + the server ("app")
dev/             files for the local stack (Postgres init script)
cmd/server/      the expense-server binary (main package)
pkg/             library packages (api, store, transaction, balance, category, user,
                 money, validate), pkg/web (embedded web page) and pkg/apidoc
                 (embedded OpenAPI spec + offline Swagger UI)
docs/            project documentation (non-code)
skills/          AI-agent skills (SKILL.md): run the server, use the API, log a transaction, raise a PR
README.md
bin/, server.log local runtime files (git-ignored)
```

Import packages as `github.com/nutmos/expensetrackr/pkg/<name>`. The web page
lives in `pkg/web` (an embed package like any other), not a root `web/` folder,
so all Go code other than `cmd/` lives under `pkg/`.

## Test

```bash
make check        # fmt-check + vet + test
# or individually
make fmt          # gofmt -s -w cmd pkg
make vet          # go vet ./...
make test         # go test ./...
```

| Target      | Does                                                        |
|-------------|-------------------------------------------------------------|
| `build`     | `go build -o bin/expense-server ./cmd/server`               |
| `run`       | `build`, then `./bin/expense-server -addr $(ADDR)` against `EXPENSE_DATABASE_URL` (default: the local Postgres) |
| `test`      | `go test ./...`                                             |
| `test-race` | `go test -race ./...` with `CGO_ENABLED=1` (needs a C compiler) |
| `vulncheck` | `govulncheck ./...`: known vulnerabilities your code can reach |
| `vet`       | `go vet ./...`                                              |
| `fmt`       | `gofmt -s -w cmd pkg`                                       |
| `fmt-check` | fails if any file needs gofmt                               |
| `check`     | `fmt-check` + `vet` + `test`                                |
| `up`        | build the image and start server + Postgres in Docker (`compose.yaml`); wait until healthy |
| `down`      | stop the Docker server and Postgres; data is kept           |
| `logs`      | follow the Docker server's logs                             |
| `db-up`     | start the local Postgres (`compose.yaml`) and wait until it is ready |
| `db-down`   | stop it; the data is kept                                   |
| `db-reset`  | stop it and **delete** its data volume                      |
| `db-psql`   | `psql` on the local development database                    |
| `clean`     | `rm -rf bin` (never touches the database)                   |
| `help`      | lists targets (default)                                     |

The Makefile exports `GOTOOLCHAIN=local` and `CGO_ENABLED=0` unless already set.

Store and handler tests need Postgres: `make db-up`, then `make test`. Each
test gets its **own empty schema** in the `expense_test` database
(`pkg/store/storetest`), dropped when the test ends, so tests run in parallel
and never touch the development data. Without `EXPENSE_TEST_DATABASE_URL`
(e.g. plain `go test` without the Makefile) those tests are **skipped** with a
message; in CI (`CI=true`) a missing URL is a failure instead.

## Container image (CI)

[`.github/workflows/publish-image.yml`](../.github/workflows/publish-image.yml)
builds the [`Dockerfile`](../Dockerfile) for `linux/amd64` and `linux/arm64`
and pushes it to `ghcr.io/nutmos/expensetrackr`:

| Trigger | What happens |
|---|---|
| The **CI** workflow succeeds on a push to `main` | Builds the commit CI tested and pushes `latest` and `sha-<commit>` |
| CI fails on `main` | Nothing is published |
| A pull request changes `Dockerfile`, `.dockerignore`, `go.mod`/`go.sum` or the workflow | Builds both platforms, **pushes nothing** |
| **Run workflow** in the Actions tab | Publishes the current `main` |

It logs in with the built-in `GITHUB_TOKEN` (`packages: write`); no secrets
are needed. The Go stage cross-compiles natively for each target
(`--platform=$BUILDPLATFORM`, `GOOS`/`GOARCH`); QEMU only runs the small
Alpine stage for arm64. Layers are cached in the GitHub Actions cache.

The package is **private** after its first publish (GitHub's default). To let
anyone `docker pull` it, open the package
(github.com/nutmos?tab=packages → `expensetrackr` → Package settings) and change
its visibility to **Public**. This is a one-time step.

## Code layout

Paths are relative to the repo root (the module root).

```
cmd/server/main.go                 entry point: flags, open DB, HTTP server, graceful shutdown
pkg/money/                         exact amounts: decimal string <-> integer minor units
  amount.go                        ParseAmount (> 0), ParseNonNegative (>= 0), ParseSigned, FormatAmount
  currency.go                      ISO 4217 codes -> minor-unit digits
  decimal.go                       DecimalInput (JSON string or number, kept as text)
pkg/validate/                      ValidationError (422) and RequestError (400)
pkg/transaction/                   transaction domain model + validation (no I/O)
  transaction.go                   Transaction, CreateInput, Validate, ParseTimestamp
  transfer.go                      ConvertTransfer: cross-currency fields of a transfer (rate lookup injected)
  input.go                         Transaction.Input (stored record -> CreateInput)
pkg/category/                      category domain model (Type expense|income, Input, Validate)
pkg/user/                          user profile model: Input, Validate, ApplyPatch, Preferences (typed JSON settings), Identity
pkg/balance/                       balance domain model + per-type validation (no I/O)
  balance.go                       Type, Balance, Input, Validate, ComputeTotals
pkg/store/                         persistence (Postgres via pgx, database/sql)
  store.go                         Store, Open, Close; errors shared by every table (ErrNotFound, ErrVersionConflict, ...), WriteOptions
  transactions.go                  table "transactions": Create, List, Get(ByUID), UpdateWith, DeleteWith
  effects.go                       applyBalanceEffects: move balances for a transaction write (same DB transaction)
  balances.go                      table "balances": CreateBalance, ListBalances, GetBalance, UpdateBalance(With), DeleteBalance(With)
  adjustments.go                   balance_adjustment transactions recorded by manual balance edits
  categories.go                    table "user_categories" (one JSON row per user): CreateCategory, ListCategories, GetCategoryByUID, UpdateCategory, DeleteCategory
  users.go                         tables "users", "user_identities": CreateUser, ListUsers, GetUserByUID, UpdateUser, DeleteUser, SetPasswordHash, CreateIdentity, ListIdentities, FindUserByIdentity
  schema.go                        baseline schema (v1): every table and index
  migrate.go                       schema versioning (schema_migrations + advisory lock), migration list, how to add one
  pgerr.go                         Postgres error codes and constraint names -> store errors
  storetest/                       per-test Postgres schema for tests (storetest.Open / URL)
  store_test.go                    fresh DB gets the baseline; reopen is a no-op; newer databases are refused untouched
pkg/api/                           Gin routes and handlers
  api.go                           router setup + shared helpers
  transactions.go                  /api/transactions routes (parseListFilter for the list query)
  exchange.go                      exchange-rate lookups (cache, bulk memo), GET /api/exchange-rates
  balances.go                      /api/balances routes
  version.go                       optimistic locking: If-Match / "version", ETag, 428 / 409
  categories.go                    /api/categories routes
  users.go                         /api/users routes (+ read-only /identities)
  pages.go                         web page routes (list / new / edit paths → index.html)
  *_test.go                        handler tests (throwaway DB per test)
pkg/web/embed.go                   embeds web/static into the binary
pkg/web/static/                    index.html (all views), style.css, router.js (history router + shared helpers),
                                   transactions.js, balances.js, categories.js (list + form page each), settings.js
pkg/apidoc/                        the API description, embedded into the binary
  openapi.yaml                     hand-written OpenAPI 3.0 spec (the source of truth for the docs)
  apidoc.go                        serves GET /api/openapi.yaml, /api/openapi.json and /swagger/
  swagger-ui/                      vendored swagger-ui-dist 5.33.1 (Apache-2.0; see VENDOR.md)
```
