# expensetrackr

A small personal-finance service: log expenses, income and transfers, and keep
your account balances up to date. It is written in Go (Gin) with a plain
HTML + vanilla JS web page and a JSON API, and stores data in **Postgres**
(pure-Go `pgx` driver, no CGO).

Status: prototype for local use. It listens on 127.0.0.1 by default and
requires a username/password login (server-side sessions); see
[Authentication](docs/auth.md). Each user's transactions, balances and
categories are private to them.

## Features

- **Transactions** of five kinds: expenses, income, transfers, asset purchases
  and returns on investment. Every kind except transfers has a category.
- **Balances** (bank accounts, cards, assets, loans) that move automatically
  with every transaction, with manual corrections recorded.
- **Multiple currencies**: exact amounts (no floats) in any ISO 4217 currency,
  and cross-currency transfers at ECB rates or your own.
- **Bulk import** of up to 500 transactions at once, safe to retry.
- **Web page** for everyday use, a **JSON API** with Swagger UI, **API tokens**
  for scripts, and **agent skills** for AI assistants.

## Quick start

With Docker running:

```bash
make up          # build and start the server + Postgres, wait until ready
```

Open http://127.0.0.1:8080/ and create the first account; the API docs are at
http://127.0.0.1:8080/swagger/. `make down` stops everything (your data is
kept); `make logs` shows the server log.

A ready-made image of the latest `main` is published as
`ghcr.io/nutmos/expensetrackr` (amd64 and arm64).

To run from source instead: `make db-up` (Postgres only), then `make run`.
Run the tests with `make test`. Details, flags and requirements:
[Getting started](docs/getting-started.md).

## Documentation

| Read this | For |
|---|---|
| [Getting started](docs/getting-started.md) | Requirements, running with Docker or from source, flags, local Postgres |
| [Development](docs/development.md) | Repository and code layout, tests, make targets, the published container image |
| [Data model](docs/data-model.md) | Transactions, types, balances, categories, users, amounts, dates, database schema |
| [API](docs/api.md) | Every endpoint and `curl` examples (live spec at `/swagger/`) |
| [Web page](docs/web.md) | How the built-in web page works |
| [AI agents](docs/agents.md) | Agent skills (run the server, use the API, log a transaction) and API tokens |
| [Authentication](docs/auth.md) | Login, sessions, API tokens, CSRF, throttling |
| [Balances](docs/balances.md) · [Categories](docs/categories.md) · [User profiles](docs/user-profile.md) | Details of each resource |
| [Exchange rates](docs/exchange-rates.md) · [Currencies](docs/currencies.md) · [Bulk import](docs/bulk-import.md) | Cross-currency transfers, currency codes, importing statements |
| [Open questions](docs/open-questions.md) | Ideas and decisions still open |

## License

expensetrackr is released under the [MIT License](LICENSE).

The vendored Swagger UI files in `pkg/apidoc/swagger-ui/` keep their own
Apache License 2.0; see the `LICENSE` and `NOTICE` files in that folder.
