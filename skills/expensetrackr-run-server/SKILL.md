---
name: expensetrackr-run-server
description: Run the expensetrackr server locally from its Dockerfile with Docker Compose (server + Postgres), then check, inspect, stop or reset it. Use when the user asks to start, run, launch, restart, rebuild, check, stop or reset expensetrackr locally, or when another expensetrackr skill needs a running server and none answers.
---

# Run expensetrackr locally (Docker Compose)

The repo builds the server from its `Dockerfile` and runs it next to Postgres
with `compose.yaml`. `scripts/run-server.sh` wraps Docker Compose with the
checks an agent needs (Docker running? port free? healthy?) and prints one
clear line per step. Use it rather than raw `docker compose` commands.

Run every command from anywhere; the script finds the repo root itself:

```bash
S=skills/expensetrackr-run-server/scripts/run-server.sh   # relative to the repo root
```

## Start

```bash
$S up                 # build the image (first time: a few minutes) and start; waits until healthy
$S up --port 18080    # if 8080 is taken
$S up --no-build      # restart without rebuilding (no code changes)
```

On success it prints the URL and the login state:

```
server: healthy at http://127.0.0.1:8080  (API docs: http://127.0.0.1:8080/swagger/)
login: no account yet: the first registration creates the owner (web page or POST /api/auth/register)
```

- **Open the web page** at the printed URL. With no account yet it shows
  "create account"; the first registration becomes the owner.
- To use the API from an agent, continue with
  [expensetrackr-api](../expensetrackr-api/SKILL.md) or
  [expensetrackr-log-transaction](../expensetrackr-log-transaction/SKILL.md)
  with `EXPENSETRACKR_URL` set to the printed URL.
- **After changing Go code**, run `$S up` again: it rebuilds the image and
  replaces the server container. Data is kept.

## Check, debug, stop

| Command | Does |
|---|---|
| `$S status` | containers and health, the server URL, and whether an account exists. Exit 1 if the server isn't healthy |
| `$S logs [N]` | last N lines of the server log (default 100) |
| `$S down` | stop the server and Postgres; **data is kept** (volume `pgdata`) |
| `$S reset --yes` | stop and **delete all data** (users, balances, transactions) |

Make targets do the same for humans: `make up`, `make down`, `make logs`
(`EXPENSE_PORT=18080 make up` for another port).

## Rules for agents

- **Never run `reset --yes` without the user's explicit OK** in this
  conversation. It deletes every account and transaction and can't be undone.
  `down` is always safe.
- **Docker not running** (`the Docker daemon is not running`): ask the user to
  start Docker Desktop (macOS: `open -a Docker`), or start it only if they
  agree. Then retry.
- **Port in use** (`port 8080 is already in use`): don't stop the process that
  holds it; it may be the user's own server. Use `$S up --port 18080` (any free
  port) and report the URL you used.
- Keep the server on `127.0.0.1` (compose publishes it there). Don't change the
  ports to `0.0.0.0` or remove the `127.0.0.1:` prefix: it has no TLS, and the
  Postgres password (`expense`) is for local use only.
- Don't write passwords or tokens into files or commit them. Create accounts
  through the web page or the API, and API tokens on the Settings page.

## What runs

| Service | Image | Host address | Notes |
|---|---|---|---|
| `app` | `expensetrackr:local`, built from `Dockerfile` | `127.0.0.1:${EXPENSE_PORT:-8080}` | Runs as a non-root user; health check `GET /api/healthz`; migrates the database on start |
| `db` | `postgres:18.6-alpine` | `127.0.0.1:${EXPENSE_PG_PORT:-5432}` | Databases `expense` (app) and `expense_test` (Go tests); data in volume `pgdata` |

The server reads `EXPENSE_DATABASE_URL` (set by compose to the `db` service)
and `EXPENSE_FX_URL` (exchange rates; set it before `up` to override).

## Troubleshooting

| Symptom | Fix |
|---|---|
| `up` fails while building with `unexpected EOF` from `proxy.golang.org` | Network hiccup inside Docker; run `$S up` again |
| `up` ends with "did not become healthy" | Read the printed logs (or `$S logs 200`). A database error usually means `db` is unhealthy: `docker compose ps` |
| `status` says "not answering" | `$S logs`, then `$S up --no-build` to restart |
| Login state "accounts exist" but you don't know them | Ask the user to log in, or (only with their explicit OK) `$S reset --yes` for a fresh start |
