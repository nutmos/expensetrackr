# Getting started

[← Back to the README](../README.md) · [All docs](README.md)

What you need, how to run expensetrackr (in Docker or from source), its
flags, and the local Postgres used for development.

## Requirements

- Go 1.26+ (`go.mod` sets the minimum `go 1.26.0` and the toolchain
  `go1.27.3`, which CI installs through `actions/setup-go`). Keep your local Go on the latest patch release:
  `make vulncheck` reports standard-library vulnerabilities of the Go you build
  with
- No CGO (`CGO_ENABLED=0` works)
- GNU make (optional; the targets are thin wrappers around `go` commands)
- Postgres 18 (16+ should work). For development, Docker with Compose v2 runs
  it for you (see [Local Postgres](#local-postgres))

## Run

### Run with Docker Compose (everything in containers)

The quickest way to run it locally: the server is built from the
`Dockerfile` and started with Postgres by `compose.yaml`. Only Docker (with
Compose v2) is needed; no local Go.

```bash
make up                          # build + start server and Postgres, wait until healthy
                                 # -> http://127.0.0.1:8080 (EXPENSE_PORT=18080 make up for another port)
make logs                        # follow the server's logs
make down                        # stop both; data is kept in the "pgdata" volume
make db-reset                    # stop both and DELETE all local data
```

(`docker compose up -d --build --wait` and `docker compose down` do the same.)
Run `make up` again after changing code: it rebuilds the image and replaces
the server container. Both ports are published on `127.0.0.1` only. The
image runs the server as a non-root user, health-checks `GET /api/healthz`
and creates or upgrades the schema on start. For agents, the
[expensetrackr-run-server](../skills/expensetrackr-run-server/SKILL.md) skill
wraps these steps with checks.

### Run the published image

Every commit on `main` that passes CI is published to GitHub Packages as
**`ghcr.io/nutmos/expensetrackr`** (tags `latest` and `sha-<commit>`, for
`linux/amd64` and `linux/arm64`) by
[`.github/workflows/publish-image.yml`](../.github/workflows/publish-image.yml).
It is the same image `make up` builds locally. To run it against the local
Postgres:

```bash
make db-up                       # Postgres from compose.yaml
docker run --rm --network expensetrackr_default -p 127.0.0.1:8080:8080 \
  -e EXPENSE_DATABASE_URL='postgres://expense:expense@db:5432/expense?sslmode=disable' \
  ghcr.io/nutmos/expensetrackr:latest
```

Point `EXPENSE_DATABASE_URL` at any Postgres to run it elsewhere. Pin
`sha-<commit>` instead of `latest` for a fixed version.

### Run from source

```bash
cd /workspace/expense-service    # repo root
make db-up                       # start the local Postgres (Docker) and wait until ready
make run                         # build bin/expense-server, serve http://127.0.0.1:8080

# the same without make
export EXPENSE_DATABASE_URL='postgres://expense:expense@127.0.0.1:5432/expense?sslmode=disable'
CGO_ENABLED=0 go build -o bin/expense-server ./cmd/server
./bin/expense-server -addr 127.0.0.1:8080

# or straight from source (EXPENSE_DATABASE_URL set as above)
go run ./cmd/server
```

`make run` takes overrides: `make run ADDR=127.0.0.1:9090`, and uses the
local development database unless `EXPENSE_DATABASE_URL` is already set.
On start the server creates or upgrades the schema (see
[Database schema](data-model.md#database-schema)).

| Flag    | Env var        | Default            | Notes                                       |
|---------|----------------|--------------------|---------------------------------------------|
| `-addr` | `EXPENSE_ADDR` | `127.0.0.1:8080`   | Keep on 127.0.0.1 unless it is behind TLS (`:8080` would expose it on all interfaces) |
| `-database-url` | `EXPENSE_DATABASE_URL` | none (required) | Postgres URL (`postgres://user:pass@host:5432/db?sslmode=…`) or key=value string. The server exits with a clear message if it is missing or unreachable; the password is never logged |
| `-fx-url` | `EXPENSE_FX_URL` | `https://api.frankfurter.dev/v1` | Frankfurter exchange-rate API for cross-currency transfers ([docs/exchange-rates.md](exchange-rates.md)) |

Set `GIN_MODE=release` to silence Gin's debug output.

Run in the background (from the repo root):

```bash
make build
EXPENSE_DATABASE_URL='postgres://…' nohup ./bin/expense-server > server.log 2>&1 &
```

Open http://127.0.0.1:8080/ for the web page, and
http://127.0.0.1:8080/swagger/ for the API documentation (Swagger UI).
The web page header links there too. The raw spec is at
http://127.0.0.1:8080/api/openapi.yaml (YAML) and
http://127.0.0.1:8080/api/openapi.json (JSON).

## Local Postgres

`compose.yaml` runs Postgres 18 for development and tests:

```bash
make db-up        # start it (needs Docker running) and wait until ready
make db-psql      # psql on the development database
make db-down      # stop it; data stays in the "pgdata" volume
make db-reset     # stop it and delete all local data
```

It creates two databases, both owned by user `expense` (password `expense`,
local only, port bound to `127.0.0.1`):

| Database       | URL (exported by the Makefile)                                               | Used for |
|----------------|------------------------------------------------------------------------------|----------|
| `expense`      | `EXPENSE_DATABASE_URL` = `postgres://expense:expense@127.0.0.1:5432/expense?sslmode=disable` | running the app |
| `expense_test` | `EXPENSE_TEST_DATABASE_URL` = `postgres://expense:expense@127.0.0.1:5432/expense_test?sslmode=disable` | `make test` |

Set `PG_PORT` (e.g. `make db-up PG_PORT=55432`) if port 5432 is taken, or
set the two URLs yourself to use another Postgres server. CI runs the same
`postgres:18.6-alpine` image as a service for the test jobs.
